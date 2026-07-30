package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mindfs/server/internal/agent"
)

// newTestProber builds a prober over an agent whose command does not exist, so
// probing fails fast and deterministically.
func newTestProber(t *testing.T) *agent.Prober {
	t.Helper()
	cfg := agent.Config{Agents: []agent.Definition{{
		Name:    "codex",
		Command: "mindfs-nonexistent-agent-binary",
	}}}
	pool := agent.NewPool(cfg)
	t.Cleanup(pool.CloseAll)
	return agent.NewProber(&cfg, pool, time.Hour)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// fakeSwitchNotifier captures the completion broadcast. Without this, a missing
// broadcast is invisible: the server log looks healthy while clients wait on
// the probe forever.
type fakeSwitchNotifier struct {
	events []agentConfigSwitchedEvent
}

func (f *fakeSwitchNotifier) AgentConfigSwitched(evt agentConfigSwitchedEvent) {
	f.events = append(f.events, evt)
}

func installFakeSwitchNotifier(t *testing.T) *fakeSwitchNotifier {
	t.Helper()
	fake := &fakeSwitchNotifier{}
	previous := switchProbeNotifier
	switchProbeNotifier = fake
	t.Cleanup(func() { switchProbeNotifier = previous })
	return fake
}

func stepByKey(steps []agentConfigSwitchStep, key string) (agentConfigSwitchStep, bool) {
	for _, step := range steps {
		if step.Key == key {
			return step, true
		}
	}
	return agentConfigSwitchStep{}, false
}

func stepKeys(steps []agentConfigSwitchStep) []string {
	keys := make([]string, 0, len(steps))
	for _, step := range steps {
		keys = append(keys, step.Key)
	}
	return keys
}

func requireStep(t *testing.T, steps []agentConfigSwitchStep, key, wantStatus string) agentConfigSwitchStep {
	t.Helper()
	step, ok := stepByKey(steps, key)
	if !ok {
		t.Fatalf("步骤 %q 缺失，实际步骤: %v", key, stepKeys(steps))
	}
	if step.Status != wantStatus {
		t.Fatalf("步骤 %q status = %q, want %q (error=%q)", key, step.Status, wantStatus, step.Error)
	}
	return step
}

// Spec §7.1 case 1: a successful switch reports every stage with real detail.
func TestSwitchReportsStepsOnSuccess(t *testing.T) {
	home := setupAgentConfigTest(t)
	first := filepath.Join(home, "one.toml")
	second := filepath.Join(home, "two.toml")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "claude",
		Name:                   "steps",
		FileSources:            []string{first, second},
		EnvLines:               []string{"A=1", "B=2", "C=3"},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsConfirm {
		t.Fatal("unexpected needs_confirm")
	}

	restore := requireStep(t, result.Steps, switchStepRestoreFiles, switchStepStatusOK)
	if restore.Count != 2 {
		t.Fatalf("restore_files.count = %d, want 2", restore.Count)
	}
	claude := requireStep(t, result.Steps, switchStepClaudeSettings, switchStepStatusOK)
	if claude.Target == "" {
		t.Fatal("claude_settings.target 应为独立 settings 路径")
	}
	env := requireStep(t, result.Steps, switchStepApplyEnv, switchStepStatusOK)
	if env.Count != 3 {
		t.Fatalf("apply_env.count = %d, want 3", env.Count)
	}
	// app == nil, so the pool/preferences-backed stages are skipped rather than run.
	requireStep(t, result.Steps, switchStepKillSessions, switchStepStatusSkipped)
	requireStep(t, result.Steps, switchStepRecordSelection, switchStepStatusSkipped)
	requireStep(t, result.Steps, switchStepProbe, switchStepStatusRunning)

	want := []string{
		switchStepRestoreFiles, switchStepClaudeSettings, switchStepApplyEnv,
		switchStepKillSessions, switchStepRecordSelection, switchStepProbe,
	}
	got := stepKeys(result.Steps)
	if len(got) != len(want) {
		t.Fatalf("步骤数量 = %d (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("步骤顺序 = %v, want %v", got, want)
		}
	}
}

// Spec §7.1 case 2: a non-isolated backup skips the claude_settings stage.
func TestSwitchSkipsClaudeSettingsStepWhenNotIsolated(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "plain", FileSources: []string{src}, EnvLines: []string{"A=1"}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireStep(t, result.Steps, switchStepClaudeSettings, switchStepStatusSkipped)
	requireStep(t, result.Steps, switchStepRestoreFiles, switchStepStatusOK)
}

// Spec §7.1 case 3: a backup without env skips the apply_env stage.
func TestSwitchSkipsEnvStepWhenBackupHasNoEnv(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "noenv", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireStep(t, result.Steps, switchStepApplyEnv, switchStepStatusSkipped)
}

// Spec §7.1 case 4: a mid-flight failure still returns the completed steps plus
// the failing one, so the UI can say where it stopped.
func TestSwitchReturnsStepsOnFailure(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "broken", FileSources: []string{src}, EnvLines: []string{"A=1"}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Delete the env backup so apply_env fails after restore_files succeeded.
	envMap, err := readAgentEnvBackups()
	if err != nil {
		t.Fatal(err)
	}
	delete(envMap, entry.ID)
	if err := writeAgentEnvBackups(envMap); err != nil {
		t.Fatal(err)
	}

	result, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil)
	if err == nil {
		t.Fatal("expected switch to fail")
	}
	requireStep(t, result.Steps, switchStepRestoreFiles, switchStepStatusOK)
	failed := requireStep(t, result.Steps, switchStepApplyEnv, switchStepStatusFailed)
	if failed.Error == "" {
		t.Fatal("失败步骤应带 error")
	}
	// The probe never started, so no probe step is appended.
	if _, ok := stepByKey(result.Steps, switchStepProbe); ok {
		t.Fatalf("失败时不应追加 probe 步骤: %v", stepKeys(result.Steps))
	}
}

// Spec §7.1 case 5: needs_confirm happens before anything is written.
func TestSwitchNeedsConfirmCarriesNoSteps(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "confirm", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsConfirm {
		t.Fatal("expected needs_confirm for an existing target file")
	}
	if len(result.Steps) != 0 {
		t.Fatalf("needs_confirm 时不应有步骤: %v", stepKeys(result.Steps))
	}
}

// Spec §7.1 case 6: the completion broadcast fires unconditionally once the
// probe finishes, including when the probe reports the agent as unavailable.
func TestSwitchProbeBroadcastsCompletion(t *testing.T) {
	fake := installFakeSwitchNotifier(t)
	prober := newTestProber(t)
	app := &AppContext{Prober: prober}

	triggerAgentConfigSwitchProbe(app, "codex", "codex-work", "work")
	waitFor(t, func() bool { return len(fake.events) > 0 })

	evt := fake.events[0]
	if evt.Agent != "codex" || evt.BackupID != "codex-work" || evt.BackupName != "work" {
		t.Fatalf("event = %+v", evt)
	}
	// The fake agent command does not exist, so the probe must report failure --
	// and the broadcast must still happen.
	if evt.Available {
		t.Fatalf("expected unavailable agent, got %+v", evt)
	}
	if evt.Error == "" {
		t.Fatal("失败时 event.Error 应非空")
	}
}
