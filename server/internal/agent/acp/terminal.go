package acp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mindfs/server/internal/commandexec"
)

// terminalManager tracks ACP terminals (terminal/create..release) that the
// agent asked this client to run. Each terminal wraps a single command
// execution via commandexec.Process and buffers its output for the agent to
// poll with terminal/output.
type terminalManager struct {
	mu         sync.RWMutex
	nextID     atomic.Int64
	terminals  map[string]*runningTerminal // terminalId -> terminal
	sessionIDs map[string][]string         // ACP session id -> terminalIds
}

// exitStatus mirrors acp.TerminalExitStatus without pulling the SDK type into
// the manager (keeps the manager testable in isolation).
type exitStatus struct {
	ExitCode int
	Signal   string
}

// runningTerminal is one in-flight (or finished) command execution.
type runningTerminal struct {
	id       string
	session  string
	proc     commandexec.Process
	output   bytes.Buffer
	truncate int // output byte limit; 0 = unlimited
	mu       sync.Mutex
	exitCode int
	signal   string
	exited   bool
	done     chan struct{}
}

// newTerminalManager creates an empty terminal manager.
func newTerminalManager() *terminalManager {
	return &terminalManager{
		terminals:  make(map[string]*runningTerminal),
		sessionIDs: make(map[string][]string),
	}
}

// create starts a command in a new terminal and returns its terminalId.
//
// The command is launched with a detached context so it keeps running after
// the ACP request that created it returns. Its lifetime is controlled by
// kill / release / closeSession / closeAll instead.
func (m *terminalManager) create(ctx context.Context, sessionID string, shellSpecs []commandexec.ShellSpec, command string, args []string, cwd string, env []string, outputByteLimit int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	commandLine := strings.TrimSpace(command)
	if commandLine == "" {
		return "", errors.New("command is required")
	}
	if len(args) > 0 {
		commandLine += " " + strings.Join(args, " ")
	}
	proc, err := commandexec.Start(context.Background(), commandexec.Options{
		Command: commandLine,
		Cwd:     cwd,
		Env:     env,
		Shells:  shellSpecs,
	})
	if err != nil {
		return "", err
	}

	id := m.newID()
	term := &runningTerminal{
		id:       id,
		session:  sessionID,
		proc:     proc,
		truncate: outputByteLimit,
		done:     make(chan struct{}),
	}

	m.mu.Lock()
	m.terminals[id] = term
	m.sessionIDs[sessionID] = append(m.sessionIDs[sessionID], id)
	m.mu.Unlock()

	go term.pump()
	return id, nil
}

// pump drains process output into the buffer, then records the exit status.
// It runs once per terminal in the background. The output channel closes when
// the process exits, which also unblocks WaitForTerminalExit via t.done.
func (t *runningTerminal) pump() {
	defer close(t.done)
	for chunk := range t.proc.Output() {
		t.appendOutput(chunk)
	}
	t.finish()
}

func (t *runningTerminal) finish() {
	result := t.proc.Wait()
	t.mu.Lock()
	t.exitCode = result.ExitCode
	t.exited = true
	if result.Err != nil {
		if msg := strings.TrimSpace(result.Err.Error()); msg != "" && t.exitCode < 0 {
			t.signal = msg
		}
	}
	t.mu.Unlock()
}

func (t *runningTerminal) appendOutput(chunk []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.truncate <= 0 {
		t.output.Write(chunk)
		return
	}
	total := t.output.Len() + len(chunk)
	if total <= t.truncate {
		t.output.Write(chunk)
		return
	}
	// Keep the tail up to the byte limit, trimmed to a UTF-8 character
	// boundary so the retained output stays valid (per ACP truncation rules).
	combined := make([]byte, 0, total)
	combined = append(combined, t.output.Bytes()...)
	combined = append(combined, chunk...)
	t.output.Reset()
	t.output.Write(trimToUTF8Boundary(combined[total-t.truncate:]))
}

// trimToUTF8Boundary drops leading continuation bytes so the slice starts at a
// fresh character boundary.
func trimToUTF8Boundary(b []byte) []byte {
	for i := 0; i < len(b); i++ {
		if b[i]&0xC0 != 0x80 {
			return b[i:]
		}
	}
	return nil
}

// output returns the buffered output, the exit status (if the command
// exited), and whether the terminal still exists.
func (m *terminalManager) output(terminalID string) (string, *exitStatus, bool, bool) {
	t := m.get(terminalID)
	if t == nil {
		return "", nil, false, false
	}
	t.mu.Lock()
	out := t.output.String()
	status := &exitStatus{ExitCode: t.exitCode, Signal: t.signal}
	exited := t.exited
	t.mu.Unlock()
	return out, status, exited, true
}

// truncated reports whether the buffered output was trimmed to the byte limit.
func (m *terminalManager) truncated(terminalID string) bool {
	t := m.get(terminalID)
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.truncate > 0 && t.output.Len() >= t.truncate
}

// waitForExit blocks until the terminal command exits (or ctx is cancelled).
// The third return value reports whether the terminal exists at all.
func (m *terminalManager) waitForExit(ctx context.Context, terminalID string) (*exitStatus, bool, bool) {
	t := m.get(terminalID)
	if t == nil {
		return nil, false, false
	}
	select {
	case <-t.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return &exitStatus{ExitCode: t.exitCode, Signal: t.signal}, true, true
	case <-ctx.Done():
		return nil, false, true
	}
}

// kill terminates the terminal process without removing it.
func (m *terminalManager) kill(terminalID string) bool {
	t := m.get(terminalID)
	if t == nil {
		return false
	}
	_ = t.proc.KillTree()
	return true
}

// release kills (if still running) and forgets a terminal.
func (m *terminalManager) release(terminalID string) bool {
	m.mu.Lock()
	t, ok := m.terminals[terminalID]
	if ok {
		delete(m.terminals, terminalID)
		ids := m.sessionIDs[t.session]
		for i, id := range ids {
			if id == terminalID {
				ids = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		if len(ids) == 0 {
			delete(m.sessionIDs, t.session)
		} else {
			m.sessionIDs[t.session] = ids
		}
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	_ = t.proc.KillTree()
	return true
}

// closeSession kills and forgets every terminal owned by the ACP session.
func (m *terminalManager) closeSession(sessionID string) {
	m.mu.Lock()
	ids := append([]string(nil), m.sessionIDs[sessionID]...)
	delete(m.sessionIDs, sessionID)
	for _, id := range ids {
		delete(m.terminals, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if t := m.get(id); t != nil {
			_ = t.proc.KillTree()
		}
	}
}

// closeAll kills and forgets every terminal.
func (m *terminalManager) closeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.terminals))
	for id := range m.terminals {
		ids = append(ids, id)
	}
	m.terminals = make(map[string]*runningTerminal)
	m.sessionIDs = make(map[string][]string)
	m.mu.Unlock()
	for _, id := range ids {
		if t := m.get(id); t != nil {
			_ = t.proc.KillTree()
		}
	}
}

func (m *terminalManager) newID() string {
	return "term-" + time.Now().Format("150405") + "-" + itoa(m.nextID.Add(1))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

func (m *terminalManager) get(terminalID string) *runningTerminal {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.terminals[terminalID]
}
