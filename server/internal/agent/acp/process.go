// Package acp provides ACP-based agent process implementation.
// All supported agents are accessed through ACP.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"mindfs/server/internal/agent/types"
	"mindfs/server/internal/commandexec"

	acp "github.com/coder/acp-go-sdk"
)

// Process manages an agent process using ACP.
// This implementation works with any ACP-compatible agent:
// - claude (via claude-code-acp wrapper)
// - gemini (via --experimental-acp flag)
// - codex (via codex-acp wrapper)
type Process struct {
	agentName string
	cmd       *exec.Cmd
	conn      *acp.ClientSideConnection
	client    *mindfsClient
	waitCh    chan error

	shells    []commandexec.ShellSpec
	terminals *terminalManager

	mu            sync.RWMutex
	sessions      map[string]*sessionState // sessionKey -> state
	sessionsByID  map[string]*sessionState // ACP session id -> state
	capability    CapabilitySnapshot
	models        *acp.SessionModelState
	modes         *acp.SessionModeState
	configOptions []acp.SessionConfigOption
	commands      []acp.AvailableCommand
	stderrHint    stderrHintState
	activePrompt  activePromptState

	elicitation   *elicitationRegistry
	activeSession activeSessionState
}

type CapabilitySnapshot struct {
	PromptSupportsAudio   bool
	PromptSupportsImage   bool
	PromptSupportsContext bool
}

type stderrHintState struct {
	mu            sync.Mutex
	expectMessage bool
	message       string
	messageAt     time.Time
}

type activePromptState struct {
	mu     sync.Mutex
	id     int64
	cancel context.CancelFunc
}

// activeSessionState tracks the most recently active ACP session on a process.
// It is used to route agent-initiated requests (like elicitation) that the Go
// SDK does not yet attach a session id to.
type activeSessionState struct {
	mu sync.Mutex
	id string
}

var stderrMessagePattern = regexp.MustCompile(`"message"\s*:\s*"([^"]+)"`)

type sessionState struct {
	ID            acp.SessionId
	models        *acp.SessionModelState
	modes         *acp.SessionModeState
	configOptions []acp.SessionConfigOption
	commands      []acp.AvailableCommand
	contextWindow types.ContextWindow
	onUpdate      func(SessionUpdate)
	mu            sync.RWMutex

	// emitMu serializes handler invocations. The SDK drains notifications on a
	// single goroutine but dispatches each id-bearing request (elicitation,
	// permission) on its own, so without this the upper-layer handler — which
	// keeps unsynchronized per-turn state — would be entered concurrently.
	emitMu sync.Mutex
}

type qwenSlashCommandNotification struct {
	SessionID   string `json:"sessionId"`
	Command     string `json:"command"`
	MessageType string `json:"messageType"`
	Message     string `json:"message"`
}

type sessionUpdateHandler func(SessionUpdate)

func (s *sessionState) setOnUpdate(onUpdate func(SessionUpdate)) {
	s.mu.Lock()
	s.onUpdate = onUpdate
	s.mu.Unlock()
}

func (s *sessionState) getOnUpdate() func(SessionUpdate) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.onUpdate
}

// emit delivers an update to the registered handler, serialized per session.
// It reports whether a handler was registered. All update delivery must go
// through this method: the upper-layer handler keeps unsynchronized per-turn
// state (aux buffer, thought buffer, response text), and the SDK invokes
// request handlers on goroutines that run concurrently with the notification
// goroutine.
func (s *sessionState) emit(update SessionUpdate) bool {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	handler := s.getOnUpdate()
	if handler == nil {
		return false
	}
	handler(update)
	return true
}

// emitHandler returns emit as a plain handler func for call sites that want to
// capture delivery and invoke it later.
func (s *sessionState) emitHandler() sessionUpdateHandler {
	return func(update SessionUpdate) { s.emit(update) }
}

func (s *sessionState) setModels(models *acp.SessionModelState) {
	s.mu.Lock()
	s.models = models
	s.mu.Unlock()
}

func (s *sessionState) getModels() *acp.SessionModelState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.models
}

func (s *sessionState) setModes(modes *acp.SessionModeState) {
	s.mu.Lock()
	s.modes = modes
	s.mu.Unlock()
}

func (s *sessionState) getModes() *acp.SessionModeState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modes
}

func (s *sessionState) setConfigOptions(options []acp.SessionConfigOption) {
	s.mu.Lock()
	s.configOptions = cloneConfigOptions(options)
	s.mu.Unlock()
}

func (s *sessionState) getConfigOptions() []acp.SessionConfigOption {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfigOptions(s.configOptions)
}

func (s *sessionState) setCommands(commands []acp.AvailableCommand) {
	s.mu.Lock()
	if len(commands) == 0 {
		s.commands = nil
	} else {
		s.commands = append([]acp.AvailableCommand(nil), commands...)
	}
	s.mu.Unlock()
}

func (s *sessionState) getCommands() []acp.AvailableCommand {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.commands) == 0 {
		return nil
	}
	return append([]acp.AvailableCommand(nil), s.commands...)
}

func (s *sessionState) setContextWindow(contextWindow types.ContextWindow) {
	s.mu.Lock()
	s.contextWindow = contextWindow
	s.mu.Unlock()
}

func (s *sessionState) getContextWindow() types.ContextWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contextWindow
}

// SessionUpdate is the internal session update type.
type SessionUpdate struct {
	Type      UpdateType
	SessionID string
	AgentName string
	Raw       acp.SessionUpdate
	// TrustedMeta carries meta that MindFS itself produced (never parsed from
	// the agent's wire payload), so it is exempt from sanitizing. Forgery is
	// impossible because this field has no JSON representation on the wire.
	TrustedMeta map[string]any
}

// UpdateType defines the type of session update.
type UpdateType string

const (
	UpdateTypeMessageChunk UpdateType = "message_chunk"
	UpdateTypeUserMessage  UpdateType = "user_message_chunk"
	UpdateTypeThoughtChunk UpdateType = "thought_chunk"
	UpdateTypeToolCall     UpdateType = "tool_call"
	UpdateTypeToolUpdate   UpdateType = "tool_update"
	UpdateTypePlan         UpdateType = "plan_update"
	UpdateTypeMessageDone  UpdateType = "message_done"
)

// mindfsClient implements acp.Client interface
type mindfsClient struct {
	proc *Process
}

func (p *Process) agentLabel() string {
	if p == nil || p.agentName == "" {
		return "unknown"
	}
	return p.agentName
}

func (p *Process) getSessionUpdateHandler(sessionID string) sessionUpdateHandler {
	session := p.getSessionByID(sessionID)
	if session == nil {
		return nil
	}
	if session.getOnUpdate() == nil {
		return nil
	}
	return session.emitHandler()
}

func (c *mindfsClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	session := c.proc.getSessionByID(string(params.SessionId))
	if session == nil {
		return nil
	}
	// Only sessions this process still tracks may become the elicitation
	// routing target; a late update for a closed session would otherwise
	// point activeSession at a session that can no longer receive anything.
	c.proc.setActiveSession(string(params.SessionId))
	if session.getOnUpdate() == nil {
		return nil
	}

	internalUpdate := wrapSessionUpdate(c.proc.agentLabel(), string(params.SessionId), params.Update)
	if params.Update.AvailableCommandsUpdate != nil {
		session.setCommands(params.Update.AvailableCommandsUpdate.AvailableCommands)
		c.proc.mu.Lock()
		c.proc.commands = append([]acp.AvailableCommand(nil), params.Update.AvailableCommandsUpdate.AvailableCommands...)
		c.proc.mu.Unlock()
	}
	if params.Update.CurrentModeUpdate != nil {
		current := params.Update.CurrentModeUpdate.CurrentModeId
		if state := session.getModes(); state != nil {
			state.CurrentModeId = current
			session.setModes(state)
			c.proc.mu.Lock()
			c.proc.modes = state
			c.proc.mu.Unlock()
		}
	}
	if params.Update.ConfigOptionUpdate != nil {
		session.setConfigOptions(params.Update.ConfigOptionUpdate.ConfigOptions)
		c.proc.mu.Lock()
		c.proc.configOptions = cloneConfigOptions(params.Update.ConfigOptionUpdate.ConfigOptions)
		c.proc.mu.Unlock()
	}
	if params.Update.UsageUpdate != nil {
		current := session.getContextWindow()
		current.ModelContextWindow = params.Update.UsageUpdate.Size
		if current.TotalTokens == 0 {
			current.TotalTokens = params.Update.UsageUpdate.Used
		}
		session.setContextWindow(current)
	}

	if internalUpdate.Type != "" {
		session.emit(internalUpdate)
	} else {
		raw, _ := json.Marshal(params.Update)
		log.Printf("[agent/acp] unhandled raw=%s", string(raw))
	}
	return nil
}

func (c *mindfsClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	// Emit a synthetic tool_call update for permission-gated operations so upper
	// layers can track tool execution and associate file paths immediately.
	if session := c.proc.getSessionByID(string(params.SessionId)); session != nil {
		toolCall := &acp.SessionUpdateToolCall{
			Content:    params.ToolCall.Content,
			Locations:  params.ToolCall.Locations,
			RawInput:   params.ToolCall.RawInput,
			RawOutput:  params.ToolCall.RawOutput,
			Title:      "",
			ToolCallId: params.ToolCall.ToolCallId,
			Status:     acp.ToolCallStatusPending,
		}
		if params.ToolCall.Title != nil {
			toolCall.Title = *params.ToolCall.Title
		}
		if params.ToolCall.Kind != nil {
			toolCall.Kind = *params.ToolCall.Kind
		} else {
			toolCall.Kind = acp.ToolKindOther
		}
		if params.ToolCall.Status != nil {
			toolCall.Status = *params.ToolCall.Status
		}
		session.emit(SessionUpdate{
			Type:      UpdateTypeToolCall,
			AgentName: c.proc.agentLabel(),
			SessionID: string(params.SessionId),
			Raw: acp.SessionUpdate{
				ToolCall: toolCall,
			},
		})
	}
	// TODO: Forward to frontend for user approval
	// For now, auto-approve with first allow option
	for _, opt := range params.Options {
		if opt.Kind == acp.PermissionOptionKindAllowOnce || opt.Kind == acp.PermissionOptionKindAllowAlways {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{
						OptionId: opt.OptionId,
					},
				},
			}, nil
		}
	}
	// Fallback to first option
	if len(params.Options) > 0 {
		return acp.RequestPermissionResponse{
			Outcome: acp.RequestPermissionOutcome{
				Selected: &acp.RequestPermissionOutcomeSelected{
					OptionId: params.Options[0].OptionId,
				},
			},
		}, nil
	}
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Cancelled: &acp.RequestPermissionOutcomeCancelled{},
		},
	}, nil
}

func (c *mindfsClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	// Agent handles file operations itself
	return acp.ReadTextFileResponse{Content: ""}, nil
}

func (c *mindfsClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}

func (c *mindfsClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	cwd := ""
	if params.Cwd != nil {
		cwd = *params.Cwd
	}
	terminalID, err := c.proc.terminals.create(
		ctx,
		string(params.SessionId),
		c.proc.shells,
		params.Command,
		params.Args,
		cwd,
		envVariablesToStrings(params.Env),
		intPtrValue(params.OutputByteLimit),
	)
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	log.Printf("[agent/acp] terminal.create agent=%s session=%s id=%s command=%q", c.proc.agentLabel(), params.SessionId, terminalID, params.Command)
	return acp.CreateTerminalResponse{TerminalId: terminalID}, nil
}

func (c *mindfsClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	output, status, exited, exists := c.proc.terminals.output(params.TerminalId)
	if !exists {
		return acp.TerminalOutputResponse{}, acp.NewInvalidParams(map[string]any{"error": "terminal not found: " + params.TerminalId})
	}
	resp := acp.TerminalOutputResponse{
		Output:    output,
		Truncated: c.proc.terminals.truncated(params.TerminalId),
	}
	if exited {
		resp.ExitStatus = &acp.TerminalExitStatus{
			ExitCode: &status.ExitCode,
			Signal:   signalPtr(status.Signal),
		}
	}
	return resp, nil
}

func (c *mindfsClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	if !c.proc.terminals.release(params.TerminalId) {
		return acp.ReleaseTerminalResponse{}, acp.NewInvalidParams(map[string]any{"error": "terminal not found: " + params.TerminalId})
	}
	log.Printf("[agent/acp] terminal.release agent=%s session=%s id=%s", c.proc.agentLabel(), params.SessionId, params.TerminalId)
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *mindfsClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	status, exited, exists := c.proc.terminals.waitForExit(ctx, params.TerminalId)
	if !exists {
		return acp.WaitForTerminalExitResponse{}, acp.NewInvalidParams(map[string]any{"error": "terminal not found: " + params.TerminalId})
	}
	if !exited {
		// Request was cancelled (ctx done) before the command finished.
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	return acp.WaitForTerminalExitResponse{
		ExitCode: &status.ExitCode,
		Signal:   signalPtr(status.Signal),
	}, nil
}

func (c *mindfsClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	if !c.proc.terminals.kill(params.TerminalId) {
		return acp.KillTerminalResponse{}, acp.NewInvalidParams(map[string]any{"error": "terminal not found: " + params.TerminalId})
	}
	log.Printf("[agent/acp] terminal.kill agent=%s session=%s id=%s", c.proc.agentLabel(), params.SessionId, params.TerminalId)
	return acp.KillTerminalResponse{}, nil
}

func (c *mindfsClient) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "_qwencode/slash_command":
		return nil, c.handleQwenSlashCommandNotification(params)
	case "_x.ai/ask_user_question":
		return c.handleXAIAskUserQuestion(ctx, params)
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (c *mindfsClient) handleQwenSlashCommandNotification(params json.RawMessage) error {
	var notif qwenSlashCommandNotification
	if err := json.Unmarshal(params, &notif); err != nil {
		return acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if strings.TrimSpace(notif.SessionID) == "" {
		return acp.NewInvalidParams(map[string]any{"error": "sessionId required"})
	}
	handler := c.proc.getSessionUpdateHandler(notif.SessionID)
	if handler == nil {
		return nil
	}
	content := notif.Message
	if content == "" {
		return nil
	}
	log.Printf("[agent/acp] ext.notification agent=%s method=_qwencode/slash_command session_id=%s command=%q message_type=%s", c.proc.agentLabel(), notif.SessionID, notif.Command, notif.MessageType)
	handler(newMessageChunkUpdate(notif.SessionID, content, map[string]any{
		"source":       "_qwencode/slash_command",
		"command":      notif.Command,
		"message_type": notif.MessageType,
	}))
	return nil
}

func newMessageChunkUpdate(sessionID, content string, meta map[string]any) SessionUpdate {
	return SessionUpdate{
		Type:      UpdateTypeMessageChunk,
		SessionID: sessionID,
		Raw: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content:       acp.TextBlock(content),
				SessionUpdate: "agent_message_chunk",
				Meta:          meta,
			},
		},
	}
}

// Start spawns an agent process with ACP mode.
func Start(ctx context.Context, agentName, command string, args []string, cwd string, env map[string]string, shells []commandexec.ShellSpec) (*Process, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	configureProcessCommand(cmd, env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	proc := &Process{
		agentName:    agentName,
		cmd:          cmd,
		sessions:     make(map[string]*sessionState),
		sessionsByID: make(map[string]*sessionState),
		waitCh:       make(chan error, 1),
		shells:       shells,
		terminals:    newTerminalManager(),
		elicitation:  newElicitationRegistry(),
	}
	proc.client = &mindfsClient{proc: proc}
	go streamProcessStderr(proc, stderr)
	go func() {
		proc.waitCh <- cmd.Wait()
	}()

	proc.conn = acp.NewClientSideConnection(proc.client, stdin, stdout)

	return proc, nil
}

// Initialize performs ACP handshake.
func (p *Process) Initialize(ctx context.Context) error {
	// Send initialize request
	resp, err := p.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Terminal: true,
			Elicitation: &acp.ElicitationCapabilities{
				Form: &acp.ElicitationFormCapabilities{},
			},
		},
		ClientInfo: &acp.Implementation{
			Name:    "mindfs",
			Version: "1.0.0",
		},
	})

	if err != nil {
		return err
	}
	if raw, err := json.Marshal(resp); err == nil {
		log.Printf("[agent/acp] initialize.resp.raw agent=%s resp=%s", p.agentLabel(), string(raw))
	}
	p.capability = CapabilitySnapshot{
		PromptSupportsAudio:   resp.AgentCapabilities.PromptCapabilities.Audio,
		PromptSupportsImage:   resp.AgentCapabilities.PromptCapabilities.Image,
		PromptSupportsContext: resp.AgentCapabilities.PromptCapabilities.EmbeddedContext,
	}
	return nil
}

// NewSession creates a new ACP session for the given MindFS session key.
func (p *Process) NewSession(ctx context.Context, sessionKey, cwd string) error {
	p.mu.Lock()
	if _, ok := p.sessions[sessionKey]; ok {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	resp, err := p.conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return err
	}
	if raw, err := json.Marshal(resp); err == nil {
		log.Printf("[agent/acp] new_session.resp.raw agent=%s session_key=%s resp=%s", p.agentLabel(), sessionKey, string(raw))
	}
	sess := &sessionState{
		ID:            resp.SessionId,
		models:        resp.Models,
		modes:         resp.Modes,
		configOptions: cloneConfigOptions(resp.ConfigOptions),
	}
	p.mu.Lock()
	if _, ok := p.sessions[sessionKey]; ok {
		p.mu.Unlock()
		return nil
	}
	if resp.Models != nil {
		p.models = resp.Models
	}
	if resp.Modes != nil {
		p.modes = resp.Modes
	}
	p.configOptions = cloneConfigOptions(resp.ConfigOptions)
	p.sessions[sessionKey] = sess
	p.sessionsByID[string(resp.SessionId)] = sess
	p.mu.Unlock()
	p.setActiveSession(string(resp.SessionId))
	return nil
}

func (p *Process) ResumeSession(ctx context.Context, sessionKey, sessionID, cwd string) error {
	p.mu.Lock()
	if _, ok := p.sessions[sessionKey]; ok {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	resp, err := p.conn.ResumeSession(ctx, acp.ResumeSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
		SessionId:  acp.SessionId(strings.TrimSpace(sessionID)),
	})
	if err != nil {
		return err
	}
	sess := &sessionState{
		ID:            acp.SessionId(strings.TrimSpace(sessionID)),
		models:        resp.Models,
		configOptions: cloneConfigOptions(resp.ConfigOptions),
	}
	if resp.Modes != nil {
		sess.modes = resp.Modes
	}
	p.mu.Lock()
	if _, ok := p.sessions[sessionKey]; ok {
		p.mu.Unlock()
		return nil
	}
	if sess.models != nil {
		p.models = sess.models
	}
	if sess.modes != nil {
		p.modes = sess.modes
	}
	p.configOptions = cloneConfigOptions(resp.ConfigOptions)
	p.sessions[sessionKey] = sess
	p.sessionsByID[string(sess.ID)] = sess
	p.mu.Unlock()
	p.setActiveSession(string(sess.ID))
	return nil
}

// SetOnUpdate registers a callback for a specific session.
func (p *Process) SetOnUpdate(sessionKey string, onUpdate func(SessionUpdate)) {
	sess := p.getSessionByKey(sessionKey)
	if sess != nil {
		sess.setOnUpdate(onUpdate)
	}
}

// SendMessage sends a prompt to a specific session.
func (p *Process) SendMessage(ctx context.Context, sessionKey, content string) error {
	start := time.Now()
	sess := p.getSessionByKey(sessionKey)

	if sess == nil {
		return nil
	}
	log.Printf("[agent/acp] send.begin agent=%s session_key=%s content=%q", p.agentLabel(), sessionKey, content)

	// Agent-initiated requests (elicitation) carry no session id, so they are
	// routed to the most recently active session. Claim it here: an agent may
	// ask before it emits any session/update for this turn, and processes are
	// shared across every session of the same agent.
	p.setActiveSession(string(sess.ID))

	promptCtx, promptCancel := context.WithCancel(ctx)
	promptID := time.Now().UnixNano()
	p.setActivePrompt(promptID, promptCancel)
	defer func() {
		p.clearActivePrompt(promptID)
		promptCancel()
	}()

	resp, err := p.conn.Prompt(promptCtx, acp.PromptRequest{
		SessionId: sess.ID,
		Prompt: []acp.ContentBlock{
			acp.TextBlock(content),
		},
	})
	if err != nil {
		return p.wrapPromptError(sessionKey, string(sess.ID), err)
	}
	if resp.Usage != nil {
		current := sess.getContextWindow()
		current.TotalTokens = resp.Usage.TotalTokens
		sess.setContextWindow(current)
	}

	// Signal completion
	sess.emit(SessionUpdate{
		Type:      UpdateTypeMessageDone,
		SessionID: string(sess.ID),
	})
	log.Printf("[agent/acp] send.done agent=%s session_key=%s duration_ms=%d", p.agentLabel(), sessionKey, time.Since(start).Milliseconds())

	return nil
}

func (p *Process) CancelCurrentTurn(sessionKey string) error {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return nil
	}
	p.releasePendingElicitations(sessionKey)
	return p.conn.Cancel(context.Background(), acp.CancelNotification{
		SessionId: sess.ID,
	})
}

// releasePendingElicitations unblocks every ask_user request waiting on a
// session. session/cancel does not cancel the SDK's inbound request contexts,
// so without this a pending elicitation blocks the agent's turn — and holds a
// handler captured from the cancelled turn — for the full elicitationTimeout.
func (p *Process) releasePendingElicitations(sessionKey string) {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil || p.elicitation == nil {
		return
	}
	p.elicitation.abandonSession(string(sess.ID))
}

// CloseSession removes a session from the process and kills any terminals it
// spawned (ACP terminals belong to a session).
func (p *Process) CloseSession(sessionKey string) {
	p.mu.Lock()
	sess, ok := p.sessions[sessionKey]
	if !ok {
		p.mu.Unlock()
		return
	}
	sessionID := string(sess.ID)
	delete(p.sessionsByID, sessionID)
	delete(p.sessions, sessionKey)
	p.mu.Unlock()

	p.elicitation.abandonSession(sessionID)
	p.clearActiveSession(sessionID)
	if p.terminals != nil {
		p.terminals.closeSession(sessionID)
	}
}

// Close terminates the process and all of its terminals.
func (p *Process) Close() error {
	if p.elicitation != nil {
		p.elicitation.abandonSession("")
	}
	if p.terminals != nil {
		p.terminals.closeAll()
	}
	p.mu.Lock()
	cmd := p.cmd
	waitCh := p.waitCh
	p.cmd = nil
	p.waitCh = nil
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	log.Printf("[agent/acp] process.close.begin agent=%s pid=%d", p.agentLabel(), pid)
	if err := killProcess(cmd.Process); err != nil && !strings.Contains(strings.ToLower(err.Error()), "process already finished") {
		log.Printf("[agent/acp] process.close.kill_error agent=%s pid=%d err=%v", p.agentLabel(), pid, err)
		return err
	}

	select {
	case err := <-waitCh:
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "signal: killed") {
			log.Printf("[agent/acp] process.close.wait_error agent=%s pid=%d err=%v", p.agentLabel(), pid, err)
			return err
		}
		log.Printf("[agent/acp] process.close.done agent=%s pid=%d", p.agentLabel(), pid)
		return nil
	case <-time.After(10 * time.Second):
		log.Printf("[agent/acp] process.close.timeout agent=%s pid=%d", p.agentLabel(), pid)
		return nil
	}
}

func killProcess(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return killProcessTree(proc)
}

// SessionID returns the ACP session ID for a MindFS session key.
func (p *Process) SessionID(sessionKey string) string {
	if sess := p.getSessionByKey(sessionKey); sess != nil {
		return string(sess.ID)
	}
	return ""
}

// Capability returns agent capabilities reported by initialize response.
func (p *Process) Capability() CapabilitySnapshot {
	return p.capability
}

func (p *Process) ConfigOptions() []acp.SessionConfigOption {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneConfigOptions(p.configOptions)
}

func (p *Process) ModelState() *acp.SessionModelState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.models
}

func (p *Process) ModeState() *acp.SessionModeState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.modes
}

func (p *Process) SetModel(ctx context.Context, sessionKey, model string) error {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil || strings.TrimSpace(model) == "" {
		return nil
	}
	options := sess.getConfigOptions()
	option, ok := findSelectConfigOption(options, acp.SessionConfigOptionCategoryModel)
	if !ok {
		if sess.getModels() == nil {
			return nil
		}
		_, err := p.conn.UnstableSetSessionModel(ctx, acp.UnstableSetSessionModelRequest{
			SessionId: sess.ID,
			ModelId:   acp.UnstableModelId(strings.TrimSpace(model)),
		})
		if err == nil {
			if state := sess.getModels(); state != nil {
				state.CurrentModelId = acp.ModelId(strings.TrimSpace(model))
				sess.setModels(state)
				p.mu.Lock()
				p.models = state
				p.mu.Unlock()
			}
		}
		return err
	}
	resp, err := p.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			ConfigId:  option.Id,
			SessionId: sess.ID,
			Value:     acp.SessionConfigValueId(strings.TrimSpace(model)),
		},
	})
	if err != nil {
		return err
	}
	sess.setConfigOptions(resp.ConfigOptions)
	p.mu.Lock()
	p.configOptions = cloneConfigOptions(resp.ConfigOptions)
	p.mu.Unlock()
	return nil
}

func (p *Process) SetMode(ctx context.Context, sessionKey, mode string) error {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil || strings.TrimSpace(mode) == "" {
		return nil
	}
	if option, ok := findSelectConfigOption(sess.getConfigOptions(), acp.SessionConfigOptionCategoryMode); ok {
		resp, err := p.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				ConfigId:  option.Id,
				SessionId: sess.ID,
				Value:     acp.SessionConfigValueId(strings.TrimSpace(mode)),
			},
		})
		if err != nil {
			return err
		}
		sess.setConfigOptions(resp.ConfigOptions)
		p.mu.Lock()
		p.configOptions = cloneConfigOptions(resp.ConfigOptions)
		p.mu.Unlock()
		return nil
	}
	_, err := p.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{
		SessionId: sess.ID,
		ModeId:    acp.SessionModeId(strings.TrimSpace(mode)),
	})
	if err == nil {
		if state := sess.getModes(); state != nil {
			state.CurrentModeId = acp.SessionModeId(strings.TrimSpace(mode))
			sess.setModes(state)
			p.mu.Lock()
			p.modes = state
			p.mu.Unlock()
		}
	}
	return err
}

func (p *Process) SetThoughtLevel(ctx context.Context, sessionKey, effort string) error {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil || strings.TrimSpace(effort) == "" {
		return nil
	}
	option, ok := findSelectConfigOption(sess.getConfigOptions(), acp.SessionConfigOptionCategoryThoughtLevel)
	if !ok {
		return nil
	}
	resp, err := p.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			ConfigId:  option.Id,
			SessionId: sess.ID,
			Value:     acp.SessionConfigValueId(strings.TrimSpace(effort)),
		},
	})
	if err != nil {
		return err
	}
	sess.setConfigOptions(resp.ConfigOptions)
	p.mu.Lock()
	p.configOptions = cloneConfigOptions(resp.ConfigOptions)
	p.mu.Unlock()
	return nil
}

func (p *Process) SessionModelState(sessionKey string) *acp.SessionModelState {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return nil
	}
	return sess.getModels()
}

func (p *Process) SessionConfigOptions(sessionKey string) []acp.SessionConfigOption {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return nil
	}
	return sess.getConfigOptions()
}

func (p *Process) SessionModeState(sessionKey string) *acp.SessionModeState {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return nil
	}
	return sess.getModes()
}

func (p *Process) SessionCommands(sessionKey string) []acp.AvailableCommand {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return nil
	}
	return sess.getCommands()
}

func (p *Process) SessionContextWindow(sessionKey string) types.ContextWindow {
	sess := p.getSessionByKey(sessionKey)
	if sess == nil {
		return types.ContextWindow{}
	}
	return sess.getContextWindow()
}

func (p *Process) RecentStderrHint() (string, bool) {
	return p.recentStderrHint()
}

func (p *Process) getSessionByKey(sessionKey string) *sessionState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[sessionKey]
}

func (p *Process) getSessionByID(sessionID string) *sessionState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessionsByID[sessionID]
}

// convertSessionUpdate converts acp-go SessionUpdate to internal format
func wrapSessionUpdate(agentName, sessionID string, update acp.SessionUpdate) SessionUpdate {
	result := SessionUpdate{
		AgentName: agentName,
		SessionID: sessionID,
		Raw:       update,
	}
	switch {
	case update.UserMessageChunk != nil:
		result.Type = UpdateTypeUserMessage
	case update.AgentMessageChunk != nil:
		result.Type = UpdateTypeMessageChunk
	case update.AgentThoughtChunk != nil:
		result.Type = UpdateTypeThoughtChunk
	case update.ToolCall != nil:
		result.Type = UpdateTypeToolCall
	case update.ToolCallUpdate != nil:
		result.Type = UpdateTypeToolUpdate
	case update.Plan != nil || update.PlanUpdate != nil:
		result.Type = UpdateTypePlan
	}
	return result
}

func streamProcessStderr(proc *Process, reader io.Reader) {
	if reader == nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Printf("[agent/acp][stderr] agent=%s %s", proc.agentLabel(), line)
		proc.captureStderrHint(line)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[agent/acp][stderr] agent=%s stream_error=%v", proc.agentLabel(), err)
	}
}

func configureProcessCommand(cmd *exec.Cmd, env map[string]string) {
	if cmd == nil {
		return
	}
	configurePlatformProcessCommand(cmd)
	if len(env) == 0 {
		return
	}
	cmd.Env = cmd.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
}

func (p *Process) wrapPromptError(sessionKey, sessionID string, err error) error {
	if hint, ok := p.recentStderrHint(); ok {
		log.Printf("[agent/acp] send.error agent=%s session_key=%s err=%v hint=%q", p.agentLabel(), sessionKey, err, hint)
		return errors.New(hint)
	}
	log.Printf("[agent/acp] send.error agent=%s session_key=%s err=%v", p.agentLabel(), sessionKey, err)
	return err
}

func envVariablesToStrings(env []acp.EnvVariable) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		out = append(out, entry.Name+"="+entry.Value)
	}
	return out
}

func intPtrValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func signalPtr(signal string) *string {
	if strings.TrimSpace(signal) == "" {
		return nil
	}
	return &signal
}

func (p *Process) captureStderrHint(line string) {
	if p == nil {
		return
	}
	p.stderrHint.mu.Lock()
	defer p.stderrHint.mu.Unlock()

	if strings.Contains(line, `"code":`) {
		p.stderrHint.expectMessage = true
		return
	}
	if !p.stderrHint.expectMessage {
		return
	}
	message, ok := parseStderrHintMessage(line)
	if !ok {
		return
	}
	p.setRecentStderrHintLocked(message)
	p.cancelActivePrompt()
}

func (p *Process) setRecentStderrHintLocked(message string) {
	p.stderrHint.message = message
	p.stderrHint.messageAt = time.Now()
	p.stderrHint.expectMessage = false
}

func parseStderrHintMessage(line string) (string, bool) {
	match := stderrMessagePattern.FindStringSubmatch(line)
	if len(match) < 2 {
		return "", false
	}
	return strings.TrimSpace(match[1]), true
}

func (p *Process) recentStderrHint() (string, bool) {
	if p == nil {
		return "", false
	}
	p.stderrHint.mu.Lock()
	defer p.stderrHint.mu.Unlock()
	if strings.TrimSpace(p.stderrHint.message) == "" {
		return "", false
	}
	if time.Since(p.stderrHint.messageAt) > 5*time.Minute {
		return "", false
	}
	return p.stderrHint.message, true
}

func (p *Process) setActivePrompt(id int64, cancel context.CancelFunc) {
	if p == nil {
		return
	}
	p.activePrompt.mu.Lock()
	p.activePrompt.id = id
	p.activePrompt.cancel = cancel
	p.activePrompt.mu.Unlock()
}

func (p *Process) clearActivePrompt(id int64) {
	if p == nil {
		return
	}
	p.activePrompt.mu.Lock()
	if p.activePrompt.id == id {
		p.activePrompt.id = 0
		p.activePrompt.cancel = nil
	}
	p.activePrompt.mu.Unlock()
}

func (p *Process) cancelActivePrompt() {
	if p == nil {
		return
	}
	p.activePrompt.mu.Lock()
	cancel := p.activePrompt.cancel
	p.activePrompt.id = 0
	p.activePrompt.cancel = nil
	p.activePrompt.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *Process) setActiveSession(id string) {
	if p == nil {
		return
	}
	p.activeSession.mu.Lock()
	p.activeSession.id = id
	p.activeSession.mu.Unlock()
}

// clearActiveSession drops the routing target when that session goes away, so
// a stale id cannot make getActiveSession return nil for still-open sessions.
func (p *Process) clearActiveSession(id string) {
	if p == nil {
		return
	}
	p.activeSession.mu.Lock()
	if p.activeSession.id == id {
		p.activeSession.id = ""
	}
	p.activeSession.mu.Unlock()
}

func (p *Process) getActiveSession() *sessionState {
	if p == nil {
		return nil
	}
	p.activeSession.mu.Lock()
	id := p.activeSession.id
	p.activeSession.mu.Unlock()
	p.mu.RLock()
	defer p.mu.RUnlock()
	if id != "" {
		if sess, ok := p.sessionsByID[id]; ok {
			return sess
		}
	}
	// The recorded session is gone (or none was recorded yet). Fall back to the
	// only live session when there is exactly one, so an agent that asks before
	// emitting any update still reaches the user.
	if len(p.sessionsByID) == 1 {
		for _, sess := range p.sessionsByID {
			return sess
		}
	}
	return nil
}

func sessionUpdateLogValue(data any) string {
	raw, err := json.Marshal(data)
	if err != nil {
		return `{"marshal_error":true}`
	}
	return string(raw)
}
