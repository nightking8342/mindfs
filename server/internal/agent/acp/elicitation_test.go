package acp

import (
	"encoding/json"
	"reflect"
	"testing"

	"mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestOrderedSchemaPropertiesRequiredFirstThenSorted(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"zeta":  map[string]any{"type": "string"},
			"alpha": map[string]any{"type": "string"},
			"beta":  map[string]any{"type": "string"},
		},
		Required: []string{"beta", "zeta"},
	}
	got := orderedSchemaProperties(schema)
	want := []string{"beta", "zeta", "alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderedSchemaProperties = %v, want %v", got, want)
	}
}

func TestElicitationSchemaToQuestionsSingleSelectAndInput(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"strategy": map[string]any{
				"type":        "string",
				"title":       "Strategy",
				"description": "How to approach the refactoring",
				"enum":        []any{"conservative", "balanced", "aggressive"},
			},
			"note": map[string]any{
				"type":        "string",
				"title":       "Note",
				"description": "Anything else",
			},
		},
		Required: []string{"strategy"},
	}
	items := elicitationSchemaToQuestions("How should I proceed?", schema)
	if len(items) != 2 {
		t.Fatalf("items = %#v, want 2 questions", items)
	}
	first := items[0]
	if first.Question != "Strategy" {
		t.Fatalf("first.Question = %q, want %q", first.Question, "Strategy")
	}
	if first.Header != "How should I proceed?" {
		t.Fatalf("first.Header = %q, want the request message", first.Header)
	}
	if len(first.Options) != 3 || first.Options[0].Label != "conservative" {
		t.Fatalf("first.Options = %#v", first.Options)
	}
	if first.MultiSelect {
		t.Fatalf("first should not be multi-select")
	}
	second := items[1]
	if second.Header != "" {
		t.Fatalf("second.Header = %q, want empty (message only on first)", second.Header)
	}
	if len(second.Options) != 0 {
		t.Fatalf("second.Options = %#v, want none", second.Options)
	}
}

func TestElicitationSchemaToQuestionsMultiSelect(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"tags": map[string]any{
				"type":  "array",
				"title": "Tags",
				"items": map[string]any{
					"type": "string",
					"enum": []any{"a", "b", "c"},
				},
			},
		},
	}
	items := elicitationSchemaToQuestions("Pick tags", schema)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if !items[0].MultiSelect {
		t.Fatalf("expected multi-select")
	}
	if len(items[0].Options) != 3 || items[0].Options[2].Label != "c" {
		t.Fatalf("options = %#v", items[0].Options)
	}
}

func TestElicitationSchemaToQuestionsFallsBackToPropertyName(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"bare": map[string]any{"type": "boolean"},
		},
	}
	items := elicitationSchemaToQuestions("", schema)
	if len(items) != 1 || items[0].Question != "bare" {
		t.Fatalf("items = %#v, want question named after property", items)
	}
}

func TestElicitationAnswersToContent(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"strategy": map[string]any{"type": "string", "enum": []any{"conservative", "balanced"}},
			"count":    map[string]any{"type": "integer"},
			"ratio":    map[string]any{"type": "number"},
			"enabled":  map[string]any{"type": "boolean"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": []any{"a", "b", "c"}},
			},
		},
		Required: []string{"strategy", "count", "ratio", "enabled", "tags"},
	}
	answers := map[string]string{
		"q_0": "balanced",
		"q_1": "42",
		"q_2": "3.5",
		"q_3": "true",
		"q_4": "a, c",
	}
	content := elicitationAnswersToContent(answers, schema)
	want := map[string]any{
		"strategy": "balanced",
		"count":    int64(42),
		"ratio":    3.5,
		"enabled":  true,
		"tags":     []string{"a", "c"},
	}
	if !reflect.DeepEqual(content, want) {
		t.Fatalf("content = %#v, want %#v", content, want)
	}
}

func TestElicitationAnswersToContentPassesThroughUnparsableScalars(t *testing.T) {
	schema := acpsdk.UnstableElicitationSchema{
		Properties: map[string]any{
			"count":   map[string]any{"type": "integer"},
			"enabled": map[string]any{"type": "boolean"},
		},
		Required: []string{"count", "enabled"},
	}
	content := elicitationAnswersToContent(map[string]string{
		"q_0": "not-a-number",
		"q_1": "maybe",
	}, schema)
	if content["count"] != "not-a-number" {
		t.Fatalf("count = %#v, want raw string passthrough", content["count"])
	}
	if content["enabled"] != "maybe" {
		t.Fatalf("enabled = %#v, want raw string passthrough", content["enabled"])
	}
}

func TestSplitMultiValue(t *testing.T) {
	got := splitMultiValue("a, b,  c ,,d")
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitMultiValue = %v, want %v", got, want)
	}
}

func TestXAIAskUserQuestionParamsJSON(t *testing.T) {
	raw := `{"questions":[{"question":"Pick one?","options":[{"label":"a","description":"Option A","preview":"p"},{"label":"b"}],"multi_select":true}]}`
	var req xAIAskUserQuestionParams
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(req.Questions) != 1 {
		t.Fatalf("questions = %#v, want 1", req.Questions)
	}
	q := req.Questions[0]
	if q.Question != "Pick one?" || !q.MultiSelectSnake {
		t.Fatalf("q = %#v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "a" || q.Options[0].Description != "Option A" || q.Options[0].Preview != "p" {
		t.Fatalf("options = %#v", q.Options)
	}
}

func TestXAIAskUserQuestionsToItems(t *testing.T) {
	req := xAIAskUserQuestionParams{
		Questions: []xAIAskUserQuestion{
			{
				Question: "Pick one",
				Options: []xAIAskUserQuestionOption{
					{Label: "a", Description: "A"},
					{Label: "b"},
				},
			},
			{
				Question:    "Pick many",
				MultiSelect: true,
				Options: []xAIAskUserQuestionOption{
					{Label: "x"},
					{Label: "y"},
				},
			},
		},
	}
	items := xAIAskUserQuestionsToItems(req)
	if len(items) != 2 {
		t.Fatalf("items = %#v, want 2", items)
	}
	if items[0].Question != "Pick one" || items[0].MultiSelect {
		t.Fatalf("items[0] = %#v", items[0])
	}
	if len(items[0].Options) != 2 || items[0].Options[0].Label != "a" || items[0].Options[0].Description != "A" {
		t.Fatalf("items[0].Options = %#v", items[0].Options)
	}
	if !items[1].MultiSelect || len(items[1].Options) != 2 {
		t.Fatalf("items[1] = %#v", items[1])
	}
}

func TestXAIAskUserQuestionsToItemsTopLevelMultiSelect(t *testing.T) {
	// The runtime's AskUserQuestionInput exposes multiSelect at the top level
	// (camelCase). When present it must apply to every question, so a
	// multi-select question survives even if the runtime does not repeat the
	// flag per question.
	raw := `{
		"questions": [
			{"question": "One?", "options": [{"label": "a"}, {"label": "b"}]},
			{"question": "Many?", "options": [{"label": "x"}, {"label": "y"}]}
		],
		"multiSelect": true
	}`
	var req xAIAskUserQuestionParams
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !req.MultiSelect {
		t.Fatalf("top-level MultiSelect not parsed")
	}
	items := xAIAskUserQuestionsToItems(req)
	if len(items) != 2 {
		t.Fatalf("items = %#v, want 2", items)
	}
	if !items[0].MultiSelect || !items[1].MultiSelect {
		t.Fatalf("top-level multiSelect should apply to every question: %#v", items)
	}
}

func TestXAIAskUserQuestionsToItemsTopLevelMultiSelectSnake(t *testing.T) {
	raw := `{"questions":[{"question":"Many?","options":[{"label":"a"}]}],"multi_select":true}`
	var req xAIAskUserQuestionParams
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !req.MultiSelectSnake {
		t.Fatalf("top-level multi_select not parsed")
	}
	items := xAIAskUserQuestionsToItems(req)
	if len(items) != 1 || !items[0].MultiSelect {
		t.Fatalf("items = %#v, want single multi-select item", items)
	}
}

func TestBuildQuestionAnswers(t *testing.T) {
	questions := []types.AskUserQuestionItem{
		{Question: "q1"},
		{Question: "q2", MultiSelect: true},
		{Question: "q3"},
	}
	answers := map[string]string{
		"q_0": "hello",
		"q_1": "a, b",
	}
	got := buildQuestionAnswers(questions, answers)
	want := map[string][]string{
		"q1": {"hello"},
		"q2": {"a", "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildQuestionAnswers = %#v, want %#v", got, want)
	}
}

// TestXAIExtResponseShape locks the AskUserQuestionExtResponse wire format:
// an internally tagged enum on "outcome" whose Accepted variant carries
// answers/partial_answers as maps keyed by question text. The runtime's serde
// deserializer rejects arrays with "invalid type: sequence, expected a map".
func TestXAIExtResponseShape(t *testing.T) {
	questions := []types.AskUserQuestionItem{
		{Question: "q1"},
		{Question: "q2", MultiSelect: true},
		{Question: "q3"},
	}
	answers := map[string]string{
		"q_0": "hello",
		"q_1": "a, b",
	}
	resp := xAIExtResponse(questions, answers)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Parse into a generic map so field presence is checked by key, not order.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["outcome"] != "accepted" {
		t.Fatalf("outcome = %#v, want %q", m["outcome"], "accepted")
	}
	am, ok := m["answers"].(map[string]any)
	if !ok {
		t.Fatalf("answers = %#v (%T), want map", m["answers"], m["answers"])
	}
	if len(am) != 2 {
		t.Fatalf("answers has %d entries, want 2", len(am))
	}
	q1, ok := am["q1"].([]any)
	if !ok || len(q1) != 1 || q1[0] != "hello" {
		t.Fatalf("answers[q1] = %#v, want [hello]", am["q1"])
	}
	q2, ok := am["q2"].([]any)
	if !ok || len(q2) != 2 || q2[0] != "a" || q2[1] != "b" {
		t.Fatalf("answers[q2] = %#v, want [a b]", am["q2"])
	}
	pm, ok := m["partial_answers"].(map[string]any)
	if !ok {
		t.Fatalf("partial_answers = %#v (%T), want map", m["partial_answers"], m["partial_answers"])
	}
	if len(pm) != 0 {
		t.Fatalf("partial_answers = %#v, want empty map", pm)
	}
}

// TestConvertEventPreservesToolCallMeta verifies that TrustedMeta (MindFS-
// produced) keys such as questions/title reach the converted event unchanged,
// while agent-supplied reserved keys are stripped.
func TestConvertEventPreservesToolCallMeta(t *testing.T) {
	trustedMeta := map[string]any{
		"questions": []types.AskUserQuestionItem{
			{
				Question: "Q?",
				Options: []types.AskUserQuestionOption{
					{Label: "A"},
					{Label: "B"},
				},
			},
		},
		"title": "Q?",
	}
	// agent tries to forge "questions" and "title"; they must be stripped.
	agentMeta := map[string]any{
		"questions": "forged",
		"title":     "forged",
		"custom":    "agent-value",
	}
	update := SessionUpdate{
		Type:        UpdateTypeToolCall,
		AgentName:   "test-agent",
		SessionID:   "sess-1",
		TrustedMeta: trustedMeta,
		Raw: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: acpsdk.ToolCallId("tc-1"),
				Kind:       acpsdk.ToolKind("ask_user"),
				Status:     acpsdk.ToolCallStatusPending,
				Title:      "Q?",
				Meta:       agentMeta,
			},
		},
	}
	ev := convertEvent(update)
	if ev.Type != types.EventTypeToolCall {
		t.Fatalf("ev.Type = %q, want %q", ev.Type, types.EventTypeToolCall)
	}
	tc, ok := ev.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("ev.Data = %#v, want types.ToolCall", ev.Data)
	}
	if tc.Meta == nil {
		t.Fatal("tool call meta was dropped by convertEvent")
	}
	// TrustedMeta values must win over agent forgeries.
	if !reflect.DeepEqual(tc.Meta["questions"], trustedMeta["questions"]) {
		t.Fatalf("tc.Meta[questions] = %#v, want trusted value", tc.Meta["questions"])
	}
	if tc.Meta["title"] != "Q?" {
		t.Fatalf("tc.Meta[title] = %#v, want trusted value", tc.Meta["title"])
	}
	// Non-reserved agent key must pass through.
	if tc.Meta["custom"] != "agent-value" {
		t.Fatalf("tc.Meta[custom] = %#v, want agent-value", tc.Meta["custom"])
	}
}

// TestConvertEventPreservesToolCallUpdateMeta verifies that TrustedMeta
// answers survive the merge and that an agent cannot forge them via Raw meta.
func TestConvertEventPreservesToolCallUpdateMeta(t *testing.T) {
	answers := map[string]string{"q_0": "A"}
	status := acpsdk.ToolCallStatusCompleted
	update := SessionUpdate{
		Type:        UpdateTypeToolUpdate,
		AgentName:   "test-agent",
		SessionID:   "sess-1",
		TrustedMeta: map[string]any{"answers": answers},
		Raw: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: acpsdk.ToolCallId("tc-1"),
				Status:     &status,
				// Agent tries to forge answers — must be stripped.
				Meta: map[string]any{"answers": map[string]string{"q_0": "forged"}},
			},
		},
	}
	ev := convertEvent(update)
	if ev.Type != types.EventTypeToolUpdate {
		t.Fatalf("ev.Type = %q, want %q", ev.Type, types.EventTypeToolUpdate)
	}
	tc, ok := ev.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("ev.Data = %#v, want types.ToolCall", ev.Data)
	}
	got, ok := tc.Meta["answers"].(map[string]string)
	if !ok || !reflect.DeepEqual(got, answers) {
		t.Fatalf("tc.Meta[answers] = %#v, want %#v", tc.Meta["answers"], answers)
	}
}

func TestXAIAskUserQuestionParamsCamelCaseMultiSelect(t *testing.T) {
	raw := `{"questions":[{"question":"Pick many?","options":[{"label":"a"},{"label":"b"}],"multiSelect":true}]}`
	var req xAIAskUserQuestionParams
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(req.Questions) != 1 || !req.Questions[0].MultiSelect {
		t.Fatalf("questions = %#v, want multiSelect=true from camelCase key", req.Questions)
	}
}
