package acp

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTerminalCreateRunsCommandAndCapturesOutput(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := m.create(ctx, "sess-1", nil, "echo", []string{"hello-acp"}, "", nil, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatal("create returned empty terminal id")
	}

	// Wait for the command to exit and capture output.
	deadline := time.Now().Add(10 * time.Second)
	var out string
	var status *exitStatus
	var exited bool
	for time.Now().Before(deadline) {
		out, status, exited, _ = m.output(id)
		if exited {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !exited {
		t.Fatal("command did not exit in time")
	}
	if !strings.Contains(out, "hello-acp") {
		t.Fatalf("output = %q, want to contain hello-acp", out)
	}
	if status == nil || status.ExitCode != 0 {
		t.Fatalf("exit status = %#v, want exit code 0", status)
	}
}

func TestTerminalWaitForExitReturnsExitCode(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := m.create(ctx, "sess-1", nil, "false", nil, "", nil, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	status, exited, exists := m.waitForExit(ctx, id)
	if !exists {
		t.Fatal("waitForExit should find the terminal")
	}
	if !exited {
		t.Fatal("waitForExit did not complete")
	}
	if status == nil || status.ExitCode == 0 {
		t.Fatalf("exit status = %#v, want non-zero exit code", status)
	}
}

func TestTerminalOutputUnknownTerminal(t *testing.T) {
	m := newTerminalManager()
	_, _, _, exists := m.output("does-not-exist")
	if exists {
		t.Fatal("output should report missing terminal")
	}
}

func TestTerminalKillTerminatesProcess(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start a long-running command, then kill it.
	id, err := m.create(ctx, "sess-1", nil, sleepCommand(), nil, "", nil, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !m.kill(id) {
		t.Fatal("kill returned false for existing terminal")
	}
	if m.kill("missing") {
		t.Fatal("kill should return false for missing terminal")
	}
	// The pump goroutine should eventually mark the terminal exited.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, exited, _ := m.output(id); exited {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("killed terminal did not exit in time")
}

func TestTerminalReleaseRemovesTerminal(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := m.create(ctx, "sess-1", nil, "echo", []string{"x"}, "", nil, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !m.release(id) {
		t.Fatal("release returned false for existing terminal")
	}
	if m.release(id) {
		t.Fatal("second release should return false")
	}
	if _, _, _, exists := m.output(id); exists {
		t.Fatal("terminal should be gone after release")
	}
}

func TestTerminalCloseSessionKillsAll(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id1, err := m.create(ctx, "sess-A", nil, sleepCommand(), nil, "", nil, 0)
	if err != nil {
		t.Fatalf("create id1: %v", err)
	}
	id2, err := m.create(ctx, "sess-A", nil, sleepCommand(), nil, "", nil, 0)
	if err != nil {
		t.Fatalf("create id2: %v", err)
	}
	if _, err := m.create(ctx, "sess-B", nil, sleepCommand(), nil, "", nil, 0); err != nil {
		t.Fatalf("create sess-B: %v", err)
	}

	m.closeSession("sess-A")
	if _, _, _, exists := m.output(id1); exists {
		t.Fatal("id1 should be gone after closeSession")
	}
	if _, _, _, exists := m.output(id2); exists {
		t.Fatal("id2 should be gone after closeSession")
	}
	// sess-B terminal should still be tracked.
	if _, _, _, exists := m.output(m.sessionFirstID(t, "sess-B")); !exists {
		t.Fatal("sess-B terminal should still exist")
	}
}

func TestTerminalCloseAllKillsEverything(t *testing.T) {
	m := newTerminalManager()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := m.create(ctx, "sess-1", nil, sleepCommand(), nil, "", nil, 0); err != nil {
		t.Fatalf("create: %v", err)
	}
	m.closeAll()
	m.mu.RLock()
	n := len(m.terminals)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatalf("closeAll left %d terminals", n)
	}
}

func TestTrimToUTF8Boundary(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc", "abc"},
		{"", ""},
		{"\xe4\xbd\xa0\xe5\xa5\xbd", "\xe4\xbd\xa0\xe5\xa5\xbd"}, // 你好 intact
		{"\xa0\xe5\xa5\xbd", "\xe5\xa5\xbd"},                      // drop a leading continuation byte
	}
	for _, tc := range cases {
		got := string(trimToUTF8Boundary([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("trimToUTF8Boundary(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTerminalOutputTruncationKeepsTail(t *testing.T) {
	term := &runningTerminal{truncate: 6}
	term.appendOutput([]byte("0123"))
	term.appendOutput([]byte("456789"))
	term.mu.Lock()
	out := term.output.String()
	term.mu.Unlock()
	if out != "456789" {
		t.Fatalf("truncated output = %q, want tail %q", out, "456789")
	}
}

func (m *terminalManager) sessionFirstID(t *testing.T, session string) string {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.sessionIDs[session]
	if len(ids) == 0 {
		t.Fatalf("no terminals for session %q", session)
	}
	return ids[0]
}

// sleepCommand returns a command that runs long enough to be killed.
// Windows cmd lacks `sleep`; PowerShell has it as an alias, but stay portable.
func sleepCommand() string {
	return "sleep 30"
}
