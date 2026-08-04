package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mindfs/server/internal/agent/types"

	acp "github.com/coder/acp-go-sdk"
)

// elicitationTimeout bounds how long an elicitation/create request waits for
// the user to answer in the frontend before giving up.
const elicitationTimeout = 10 * time.Minute

// pendingElicitation tracks an in-flight elicitation/create request that is
// waiting for the user to answer in the frontend. The synthetic tool-call ID
// doubles as the key the frontend uses to submit answers back.
type pendingElicitation struct {
	toolCallID string
	sessionID  string
	resultCh   chan elicitationResult
	// abandonCh is closed when the turn is cancelled or the session goes away,
	// releasing askUser without waiting out the full timeout.
	abandonCh chan struct{}
}

// abandon releases a waiting askUser. Safe to call once per pending entry; the
// registry guarantees that by removing the entry before abandoning it.
func (p *pendingElicitation) abandon() {
	close(p.abandonCh)
}

// elicitationResult carries the user's answers back to the blocked
// UnstableCreateElicitation / _x.ai/ask_user_question call.
type elicitationResult struct {
	toolCallID string
	answers    map[string]string
}

// elicitationRegistry maps synthetic tool-call IDs to pending elicitations.
// One registry lives on each Process.
type elicitationRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingElicitation
	seq     int64
	// idPrefix disambiguates ids across agent process restarts: the counter
	// resets with the process, but ids are persisted as tool-call ids in
	// session history, so a bare counter would collide with already-answered
	// entries and the frontend would merge the new question into the old one.
	idPrefix string
}

func newElicitationRegistry() *elicitationRegistry {
	return &elicitationRegistry{
		pending:  make(map[string]*pendingElicitation),
		idPrefix: randomElicitationPrefix(),
	}
}

// randomElicitationPrefix returns a short random token unique to one registry.
func randomElicitationPrefix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to the clock; uniqueness across restarts is all that matters.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func (r *elicitationRegistry) register(p *pendingElicitation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[p.toolCallID] = p
}

func (r *elicitationRegistry) resolve(toolCallID string) (*pendingElicitation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[toolCallID]
	if ok {
		delete(r.pending, toolCallID)
	}
	return p, ok
}

// abandonSession releases every pending elicitation belonging to sessionID.
// A blank sessionID abandons all of them (process shutdown).
func (r *elicitationRegistry) abandonSession(sessionID string) {
	r.mu.Lock()
	abandoned := make([]*pendingElicitation, 0, len(r.pending))
	for id, p := range r.pending {
		if sessionID != "" && p.sessionID != sessionID {
			continue
		}
		delete(r.pending, id)
		abandoned = append(abandoned, p)
	}
	r.mu.Unlock()
	for _, p := range abandoned {
		p.abandon()
	}
}

func (r *elicitationRegistry) nextToolCallID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return fmt.Sprintf("elic_%s_%d", r.idPrefix, r.seq)
}

// UnstableCreateElicitation implements the ACP elicitation/create request.
//
// Form-mode requests are surfaced to the frontend as a synthetic ask_user tool
// call (reusing the existing AskUserQuestionCard UI) and block until the user
// answers, the request is cancelled, or a timeout elapses. The user's answers
// are returned as the request's content, shaped according to the requested
// JSON schema.
//
// URL-mode requests are not advertised in client capabilities and therefore
// rejected with invalid params if an agent sends one anyway.
func (c *mindfsClient) UnstableCreateElicitation(ctx context.Context, params acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	proc := c.proc
	if params.Form == nil {
		if params.Url != nil {
			log.Printf("[agent/acp] elicitation.url_unsupported agent=%s message=%q url=%q", proc.agentLabel(), params.Url.Message, params.Url.Url)
		}
		return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(map[string]any{"error": "url elicitation is not supported"})
	}
	form := params.Form

	isConfirm := false
	questions := elicitationSchemaToQuestions(form.Message, form.RequestedSchema)
	if len(questions) == 0 {
		if strings.TrimSpace(form.Message) == "" {
			return acp.UnstableCreateElicitationResponse{}, acp.NewInvalidParams(map[string]any{"error": "requestedSchema has no form fields"})
		}
		// Message-only confirmation: the schema declares no fields (Properties
		// defaults to {}), but the request is still answerable as a yes/no.
		isConfirm = true
		questions = []types.AskUserQuestionItem{{
			Question: strings.TrimSpace(form.Message),
			Options:  []types.AskUserQuestionOption{{Label: "Yes"}, {Label: "No"}},
		}}
	}

	result, err := proc.askUser(ctx, form.Message, questions)
	if err != nil {
		// A declined outcome is a protocol response, not a transport failure:
		// returning a JSON-RPC error here makes agents treat the elicitation as
		// a broken client and abort the whole turn.
		if errors.Is(err, errElicitationDeclined) {
			log.Printf("[agent/acp] elicitation.declined agent=%s err=%v", proc.agentLabel(), err)
			return acp.NewUnstableCreateElicitationResponseDecline(), nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return acp.NewUnstableCreateElicitationResponseCancel(), nil
		}
		return acp.UnstableCreateElicitationResponse{}, err
	}
	content := elicitationAnswersToContent(result.answers, form.RequestedSchema)
	if isConfirm {
		if isAffirmative(result.answers["q_0"]) {
			log.Printf("[agent/acp] elicitation.confirm.accept agent=%s tool_call_id=%s", proc.agentLabel(), result.toolCallID)
			return acp.UnstableCreateElicitationResponse{
				Accept: &acp.UnstableCreateElicitationAccept{
					Action:  "accept",
					Content: content,
				},
			}, nil
		}
		log.Printf("[agent/acp] elicitation.confirm.decline agent=%s tool_call_id=%s", proc.agentLabel(), result.toolCallID)
		return acp.NewUnstableCreateElicitationResponseDecline(), nil
	}
	log.Printf("[agent/acp] elicitation.accepted agent=%s tool_call_id=%s fields=%d", proc.agentLabel(), result.toolCallID, len(content))
	return acp.UnstableCreateElicitationResponse{
		Accept: &acp.UnstableCreateElicitationAccept{
			Action:  "accept",
			Content: content,
		},
	}, nil
}

// isAffirmative reports whether a confirmation answer means yes. The frontend
// renders the synthetic confirm question with literal "Yes"/"No" options, so
// the answer is usually one of those; the synonyms tolerate relabeling.
func isAffirmative(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "yes", "y", "true", "1":
		return true
	}
	return false
}

// UnstableCompleteElicitation implements the ACP elicitation/complete
// notification. URL-mode elicitations are unsupported, so this is only a
// bookkeeping log for agents that send the notification anyway.
func (c *mindfsClient) UnstableCompleteElicitation(ctx context.Context, params acp.UnstableCompleteElicitationNotification) error {
	log.Printf("[agent/acp] elicitation.complete agent=%s elicitation_id=%s", c.proc.agentLabel(), params.ElicitationId)
	return nil
}

// errElicitationDeclined marks the outcomes where no answer will ever arrive
// but the agent is not at fault: no session to render the card in, the user
// cancelled the turn, the session closed, or the request timed out. Callers
// translate it into the protocol's decline/cancel response instead of a
// JSON-RPC error, which agents read as a client failure and abort the turn on.
var errElicitationDeclined = errors.New("elicitation declined")

// askUser is the shared pipeline behind both the standard ACP
// elicitation/create request and the xAI _x.ai/ask_user_question extension
// method. It surfaces questions to the frontend as a synthetic ask_user tool
// call (reusing the existing AskUserQuestionCard UI) and blocks until the user
// answers, the request is cancelled, or a timeout elapses.
//
// The request is routed to the session that is currently active on this
// process: the Go SDK does not yet surface the elicitation scope (sessionId)
// to handlers, so both entry points fall back to the most recently active
// session.
func (p *Process) askUser(ctx context.Context, title string, questions []types.AskUserQuestionItem) (elicitationResult, error) {
	sess := p.getActiveSession()
	if sess == nil {
		return elicitationResult{}, fmt.Errorf("%w: no active session to route ask_user_question", errElicitationDeclined)
	}
	sessionID := string(sess.ID)

	toolCallID := p.elicitation.nextToolCallID()
	pending := &pendingElicitation{
		toolCallID: toolCallID,
		sessionID:  sessionID,
		resultCh:   make(chan elicitationResult, 1),
		abandonCh:  make(chan struct{}),
	}
	p.elicitation.register(pending)

	delivered := sess.emit(SessionUpdate{
		Type:      UpdateTypeToolCall,
		AgentName: p.agentLabel(),
		SessionID: sessionID,
		Raw: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: acp.ToolCallId(toolCallID),
				Kind:       acp.ToolKind("ask_user"),
				Status:     acp.ToolCallStatusPending,
				Title:      title,
			},
		},
		// questions/title are MindFS-produced and must not pass through agent
		// meta sanitization; use TrustedMeta so mergeToolCallMeta keeps them.
		TrustedMeta: map[string]any{
			"questions": questions,
			"title":     title,
		},
	})
	if !delivered {
		p.elicitation.resolve(toolCallID)
		return elicitationResult{}, fmt.Errorf("%w: session has no update handler", errElicitationDeclined)
	}
	log.Printf("[agent/acp] elicitation.begin agent=%s session=%s tool_call_id=%s fields=%d", p.agentLabel(), sessionID, toolCallID, len(questions))

	// finish marks the synthetic card complete. answers is nil on every
	// non-answered outcome, which collapses the card without persisting one.
	finish := func(answers map[string]string) {
		sess.emit(toolCallCompleteUpdate(p.agentLabel(), sessionID, toolCallID, answers))
	}

	select {
	case result := <-pending.resultCh:
		finish(result.answers)
		return result, nil
	case <-pending.abandonCh:
		// The turn was cancelled or the session closed; the registry already
		// dropped the entry.
		finish(nil)
		return elicitationResult{}, fmt.Errorf("%w: session ended before an answer arrived", errElicitationDeclined)
	case <-ctx.Done():
		result, answered := p.drainResolvedElicitation(pending, toolCallID)
		if answered {
			finish(result.answers)
			return result, nil
		}
		finish(nil)
		return elicitationResult{}, ctx.Err()
	case <-time.After(elicitationTimeout):
		result, answered := p.drainResolvedElicitation(pending, toolCallID)
		if answered {
			finish(result.answers)
			return result, nil
		}
		finish(nil)
		log.Printf("[agent/acp] elicitation.timeout agent=%s session=%s tool_call_id=%s", p.agentLabel(), sessionID, toolCallID)
		return elicitationResult{}, fmt.Errorf("%w: timed out after %s", errElicitationDeclined, elicitationTimeout)
	}
}

// drainResolvedElicitation removes the pending entry and reports an answer that
// landed in the same instant the timeout or cancellation fired. Without it the
// answer would be dropped even though answerElicitation already reported
// success to the frontend and history recorded the question as answered.
func (p *Process) drainResolvedElicitation(pending *pendingElicitation, toolCallID string) (elicitationResult, bool) {
	p.elicitation.resolve(toolCallID)
	select {
	case result := <-pending.resultCh:
		return result, true
	default:
		return elicitationResult{}, false
	}
}

// xAIAskUserQuestionParams mirrors the parameter shape of the xAI
// ask_user_question tool, which Grok's runtime sends over the ACP channel as
// the private extension method _x.ai/ask_user_question.
//
// The runtime's AskUserQuestionInput carries multiSelect at the top level
// (camelCase, per the tool schema), while the per-question AskUserQuestion
// struct also accepts multiSelect/multi_select. Both spellings at both levels
// are accepted so the flag survives whichever shape the runtime serializes.
type xAIAskUserQuestionParams struct {
	Questions []xAIAskUserQuestion `json:"questions"`
	// MultiSelect is a top-level switch applying to every question. The
	// runtime's AskUserQuestionInput exposes it as the camelCase field
	// "multiSelect"; the snake_case spelling is accepted for compatibility.
	MultiSelect      bool `json:"multiSelect,omitempty"`
	MultiSelectSnake bool `json:"multi_select,omitempty"`
}

type xAIAskUserQuestion struct {
	Question    string                     `json:"question"`
	Header      string                     `json:"header,omitempty"`
	Options     []xAIAskUserQuestionOption `json:"options,omitempty"`
	MultiSelect bool                       `json:"multiSelect,omitempty"`
	// MultiSelectSnake accepts the snake_case spelling of multiSelect for
	// compatibility with clients that serialize the tool schema verbatim.
	MultiSelectSnake bool `json:"multi_select,omitempty"`
}

type xAIAskUserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

// handleXAIAskUserQuestion bridges the xAI private _x.ai/ask_user_question
// extension method onto the same elicitation pipeline as the standard ACP
// elicitation/create request. Grok's runtime does not speak the standard
// elicitation method yet, so without this case it would always hit
// "Method not found".
//
// The JSON-RPC result must be an AskUserQuestionExtResponse: an internally
// tagged enum on "outcome". MindFS always submits the user's answers, so the
// response is the Accepted variant carrying the per-question answers.
func (c *mindfsClient) handleXAIAskUserQuestion(ctx context.Context, params json.RawMessage) (any, error) {
	var req xAIAskUserQuestionParams
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if len(req.Questions) == 0 {
		return nil, acp.NewInvalidParams(map[string]any{"error": "questions required"})
	}

	questions := xAIAskUserQuestionsToItems(req)
	title := questions[0].Question
	result, err := c.proc.askUser(ctx, title, questions)
	if err != nil {
		// SkipInterview is the runtime's "user did not answer" outcome. A
		// JSON-RPC error here reads as a broken client and aborts the turn.
		if errors.Is(err, errElicitationDeclined) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[agent/acp] ext.ask_user_question.skipped agent=%s err=%v", c.proc.agentLabel(), err)
			return xAIExtSkipResponse(), nil
		}
		return nil, err
	}
	log.Printf("[agent/acp] ext.ask_user_question.accepted agent=%s tool_call_id=%s answered=%d", c.proc.agentLabel(), result.toolCallID, len(result.answers))
	return xAIExtResponse(questions, result.answers), nil
}

// xAIExtResponse builds the AskUserQuestionExtResponse JSON-RPC result the xAI
// runtime expects back from _x.ai/ask_user_question: the Accepted outcome
// carrying the user's answers. The runtime's serde deserializer requires
// answers/partial_answers to be maps keyed by question text (arrays are
// rejected with "invalid type: sequence, expected a map"). partial_answers is
// always empty because MindFS submits whatever the frontend collected.
func xAIExtResponse(questions []types.AskUserQuestionItem, answers map[string]string) map[string]any {
	return map[string]any{
		"outcome":         "accepted",
		"answers":         buildQuestionAnswers(questions, answers),
		"partial_answers": map[string][]string{},
	}
}

// xAIExtSkipResponse builds the SkipInterview outcome of
// AskUserQuestionExtResponse, the runtime's "no answer was collected" variant.
// It is an empty struct variant, so "outcome" is the only field.
func xAIExtSkipResponse() map[string]any {
	return map[string]any{"outcome": "skip_interview"}
}

// xAIAskUserQuestionsToItems converts the xAI ask_user_question parameter
// shape into the AskUserQuestionItem shape the frontend card renders. A
// top-level multiSelect (either spelling) applies to every question; a
// per-question flag wins where present.
func xAIAskUserQuestionsToItems(req xAIAskUserQuestionParams) []types.AskUserQuestionItem {
	topLevelMulti := req.MultiSelect || req.MultiSelectSnake
	items := make([]types.AskUserQuestionItem, 0, len(req.Questions))
	for _, q := range req.Questions {
		item := types.AskUserQuestionItem{
			Question:    q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect || q.MultiSelectSnake || topLevelMulti,
		}
		for _, opt := range q.Options {
			item.Options = append(item.Options, types.AskUserQuestionOption{Label: opt.Label, Description: opt.Description})
		}
		items = append(items, item)
	}
	return items
}

// buildQuestionAnswers maps the frontend's positional answers (q_0, q_1, ...)
// back into the answers map the AskUserQuestionExtResponse::Accepted variant
// expects: keyed by the question text, each value the selected option(s) as an
// array of strings. Unanswered questions are omitted.
func buildQuestionAnswers(questions []types.AskUserQuestionItem, answers map[string]string) map[string][]string {
	out := make(map[string][]string, len(questions))
	for i, q := range questions {
		value, ok := answers[fmt.Sprintf("q_%d", i)]
		if !ok {
			continue
		}
		var parts []string
		if q.MultiSelect {
			// Multi-select answers arrive as a single ", "-joined string.
			// Option labels may themselves contain commas, so split by the
			// known labels rather than blindly on every comma.
			labels := make([]string, 0, len(q.Options))
			for _, opt := range q.Options {
				labels = append(labels, opt.Label)
			}
			parts = splitMultiValueWithOptions(value, labels)
		} else {
			parts = []string{value}
		}
		if existing, ok := out[q.Question]; ok {
			// Two questions may share the same text; the serde map keyed by
			// question text would silently drop the later answer, so merge.
			log.Printf("[agent/acp] ext.ask_user_question.dup_question question=%q merged=%d", q.Question, len(parts))
			out[q.Question] = mergeStringLists(existing, parts)
		} else {
			out[q.Question] = parts
		}
	}
	return out
}

// mergeStringLists concatenates two lists, dropping duplicates while keeping
// first-occurrence order.
func mergeStringLists(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// answerElicitation resolves a pending elicitation with the user's answers.
// It is invoked by session.AnswerQuestion, which the frontend reaches through
// the existing session.answer_question WebSocket flow.
func (p *Process) answerElicitation(ctx context.Context, answer types.AskUserAnswer) error {
	id := strings.TrimSpace(answer.ToolUseID)
	if id == "" {
		return errors.New("tool_use_id required")
	}
	pending, ok := p.elicitation.resolve(id)
	if !ok {
		return fmt.Errorf("no pending elicitation for tool use id %q", id)
	}
	select {
	case pending.resultCh <- elicitationResult{toolCallID: id, answers: answer.Answers}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// toolCallCompleteUpdate builds the tool_call_update event that marks the
// synthetic ask_user tool call as completed so the frontend collapses the card
// and, when answers exist, shows them as persisted.
//
// Kind must be set: the kanban aux-flag hook keys off ToolKindAskUser to clear
// the task's "waiting for user" flag, and tool_call_update carries no kind of
// its own, so omitting it leaves the task flagged forever.
func toolCallCompleteUpdate(agentName, sessionID, toolCallID string, answers map[string]string) SessionUpdate {
	status := acp.ToolCallStatusCompleted
	kind := acp.ToolKind("ask_user")
	// answers are MindFS-produced; use TrustedMeta so mergeToolCallMeta
	// keeps them without sanitization. Kind must stay in Raw so the kanban
	// aux-flag hook can clear the "waiting for user" flag.
	var trustedMeta map[string]any
	if len(answers) > 0 {
		trustedMeta = map[string]any{"answers": answers}
	}
	return SessionUpdate{
		Type:      UpdateTypeToolUpdate,
		AgentName: agentName,
		SessionID: sessionID,
		Raw: acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: acp.ToolCallId(toolCallID),
				Status:     &status,
				Kind:       &kind,
			},
		},
		TrustedMeta: trustedMeta,
	}
}

// orderedSchemaProperties returns the schema property names in a stable order:
// required properties first (in schema order), then remaining properties
// sorted by name. The frontend submits answers with positional keys
// (q_0, q_1, ...), so both question rendering and response content mapping
// must agree on this order.
func orderedSchemaProperties(schema acp.UnstableElicitationSchema) []string {
	seen := make(map[string]bool, len(schema.Properties))
	order := make([]string, 0, len(schema.Properties))
	for _, name := range schema.Required {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := schema.Properties[name]; !ok {
			continue
		}
		seen[name] = true
		order = append(order, name)
	}
	rest := make([]string, 0, len(schema.Properties)-len(order))
	for name := range schema.Properties {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

// elicitationField is a flattened view of one property in an elicitation
// requestedSchema.
type elicitationField struct {
	name        string
	title       string
	description string
	kind        string
	enum        []string // labels shown to the user
	enumValues  []string // const values returned to the agent; equals enum for plain enum
	multiSelect bool
}

func parseElicitationField(name string, raw any) elicitationField {
	field := elicitationField{name: name, kind: "string"}
	m, ok := raw.(map[string]any)
	if !ok {
		return field
	}
	field.title = stringValue(m["title"])
	field.description = stringValue(m["description"])
	if kind, ok := m["type"].(string); ok && strings.TrimSpace(kind) != "" {
		field.kind = strings.ToLower(strings.TrimSpace(kind))
	}
	// enum wins; oneOf is the alternative encoding ({const, title} pairs)
	// with labels shown to the user and consts returned to the agent.
	if values, ok := stringSlice(m["enum"]); ok {
		field.enum = values
		field.enumValues = values
	} else if labels, values := parseConstOptions(m["oneOf"]); len(labels) > 0 {
		field.enum = labels
		field.enumValues = values
	}
	if field.kind == "array" {
		field.multiSelect = true
		if items, ok := m["items"].(map[string]any); ok {
			if values, ok := stringSlice(items["enum"]); ok {
				field.enum = values
				field.enumValues = values
			} else if labels, values := parseConstOptions(items["anyOf"]); len(labels) > 0 {
				field.enum = labels
				field.enumValues = values
			}
		}
	}
	return field
}

// parseConstOptions reads a oneOf/anyOf array of {const, title} objects into
// parallel label/value slices. The label (title, falling back to the const)
// is what the user sees and picks; the value (const) is what is returned to
// the agent. Elements without a const are skipped.
func parseConstOptions(v any) (labels, values []string) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		val, ok := m["const"]
		if !ok {
			continue
		}
		value := scalarToString(val)
		label := strings.TrimSpace(stringValue(m["title"]))
		if label == "" {
			label = value
		}
		labels = append(labels, label)
		values = append(values, value)
	}
	return labels, values
}

// elicitationSchemaToQuestions converts an elicitation requestedSchema into the
// AskUserQuestionItem shape the frontend AskUserQuestionCard renders. String
// fields with an enum become single-select options, array fields become
// multi-select options, and everything else renders as a free-text input.
func elicitationSchemaToQuestions(message string, schema acp.UnstableElicitationSchema) []types.AskUserQuestionItem {
	order := orderedSchemaProperties(schema)
	items := make([]types.AskUserQuestionItem, 0, len(order))
	for i, name := range order {
		field := parseElicitationField(name, schema.Properties[name])
		question := field.title
		if question == "" {
			question = field.description
		}
		if question == "" {
			question = name
		}
		item := types.AskUserQuestionItem{Question: question}
		if i == 0 && strings.TrimSpace(message) != "" {
			item.Header = strings.TrimSpace(message)
		}
		if len(field.enum) > 0 {
			item.Options = make([]types.AskUserQuestionOption, 0, len(field.enum))
			for _, value := range field.enum {
				item.Options = append(item.Options, types.AskUserQuestionOption{Label: value})
			}
		}
		item.MultiSelect = field.multiSelect
		items = append(items, item)
	}
	return items
}

// elicitationAnswersToContent maps the frontend's positional answers
// (q_0, q_1, ...) back to an object shaped according to the requested schema.
func elicitationAnswersToContent(answers map[string]string, schema acp.UnstableElicitationSchema) map[string]any {
	order := orderedSchemaProperties(schema)
	content := make(map[string]any)
	for i, name := range order {
		value, ok := answers[fmt.Sprintf("q_%d", i)]
		if !ok {
			continue
		}
		field := parseElicitationField(name, schema.Properties[name])
		if field.multiSelect {
			// Option labels may themselves contain commas, so split by the
			// known labels (from the field's enum) instead of on every comma.
			parts := splitMultiValueWithOptions(value, field.enum)
			// Map display labels back to schema values (oneOf/anyOf consts);
			// labels and values are identical for a plain enum.
			if field.enumValues != nil {
				for j, part := range parts {
					parts[j] = labelToValue(part, field.enum, field.enumValues)
				}
			}
			if len(parts) > 0 {
				content[name] = parts
			}
		} else {
			if field.enumValues != nil {
				value = labelToValue(value, field.enum, field.enumValues)
			}
			content[name] = convertScalarValue(value, field.kind)
		}
	}
	return content
}

func splitMultiValue(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// splitMultiValueWithOptions splits a ", "-joined multi-select answer, using
// the known option labels to reconstruct labels that themselves contain
// commas. The frontend joins selections with ", ", which corrupts labels like
// "red, green" into "red" and "green". When the valid labels are known we
// recover them; when they are not, we fall back to the naive comma split so
// free-text input stays permissive.
func splitMultiValueWithOptions(value string, validLabels []string) []string {
	if len(validLabels) == 0 {
		return splitMultiValue(value)
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	// Exact whole-string match: covers single-select answers whose label
	// contains a comma, so they are not split into fragments.
	for _, label := range validLabels {
		if trimmed == label {
			return []string{label}
		}
	}
	// Longest-match reconstruction. Split on "," and try to rejoin adjacent
	// fragments with "," (the frontend used ", ", whose space survives the
	// split as a leading space on the next fragment) to recover a label.
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	i := 0
	for i < len(parts) {
		matched := false
		for j := len(parts) - i; j >= 2; j-- {
			cand := strings.TrimSpace(strings.Join(parts[i:i+j], ","))
			for _, label := range validLabels {
				if cand == label {
					out = append(out, cand)
					i += j
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			if t := strings.TrimSpace(parts[i]); t != "" {
				out = append(out, t)
			}
			i++
		}
	}
	return out
}

// convertScalarValue coerces a free-text answer into the JSON type declared by
// the schema field. Values that fail to parse are passed through as strings.
func convertScalarValue(value, kind string) any {
	switch kind {
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		}
		return value
	case "number":
		if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return f
		}
		return value
	case "integer":
		if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return n
		}
		return value
	default:
		return value
	}
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringSlice(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, scalarToString(item))
	}
	return out, true
}

// scalarToString renders a JSON scalar (string, number, bool) the way the
// frontend receives it after JSON round-tripping.
func scalarToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// labelToValue maps a display label back to the schema value (const) for fields
// declared with oneOf/anyOf. Unknown labels (free text) pass through unchanged.
func labelToValue(label string, labels, values []string) string {
	for i, l := range labels {
		if l == label {
			return values[i]
		}
	}
	return label
}
