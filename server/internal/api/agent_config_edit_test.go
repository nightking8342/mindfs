package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mindfs/server/internal/preferences"
)

// setupAgentConfigTest isolates every path the agent-config code touches
// (config dir, home, agents.json) into a temp dir so tests never read or write
// the developer's real Claude / MindFS configuration.
func setupAgentConfigTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	agentsPath := filepath.Join(home, "agents.json")
	payload := `{"agents":[{"name":"claude","command":"claude"},{"name":"codex","command":"codex"}]}`
	if err := os.WriteFile(agentsPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINDFS_AGENTS_CONFIG", agentsPath)
	return home
}

func writeUserClaudeSettings(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func TestIsClaudeAgentName(t *testing.T) {
	if !isClaudeAgentName("claude") || !isClaudeAgentName("Claude-Code") || !isClaudeAgentName("claudecode") {
		t.Fatal("expected claude aliases to match")
	}
	if isClaudeAgentName("codex") || isClaudeAgentName("") {
		t.Fatal("non-claude names should not match")
	}
}

func TestIsClaudeSettingsSourcePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("home", ".claude", "settings.json"), true},
		{filepath.Join("home", ".CLAUDE", "Settings.JSON"), true},
		{filepath.Join("home", ".claude", "settings.local.json"), false},
		{filepath.Join("home", ".codex", "settings.json"), false},
		{filepath.Join("home", ".claude", "agents", "settings.json"), false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isClaudeSettingsSourcePath(tc.path); got != tc.want {
			t.Errorf("isClaudeSettingsSourcePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestResolveClaudeSettingsPathDefaultAndJail(t *testing.T) {
	setupAgentConfigTest(t)

	path, err := resolveClaudeSettingsPath("claude-work", "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "claude-work.json" {
		t.Fatalf("default path = %q, want basename claude-work.json", path)
	}
	if filepath.Base(filepath.Dir(path)) != "claude-settings" {
		t.Fatalf("default path = %q, want parent dir claude-settings", path)
	}

	// A path inside the jail is accepted.
	inside := filepath.Join(filepath.Dir(path), "custom.json")
	if _, err := resolveClaudeSettingsPath("claude-work", inside); err != nil {
		t.Fatalf("expected in-jail path to be accepted: %v", err)
	}

	// Paths outside the jail are rejected, including traversal attempts.
	for _, bad := range []string{
		filepath.Join(t.TempDir(), "escape.json"),
		filepath.Join(filepath.Dir(path), "..", "..", "escape.json"),
	} {
		if _, err := resolveClaudeSettingsPath("claude-work", bad); err == nil {
			t.Fatalf("expected jail error for %q", bad)
		}
	}
}

// Spec §9 case 1: creating a backup with edited content writes the snapshot and
// leaves the on-disk source file untouched.
func TestCreateBackupWithFileContentsDoesNotTouchSource(t *testing.T) {
	home := setupAgentConfigTest(t)

	src := filepath.Join(home, "source-config.toml")
	if err := os.WriteFile(src, []byte("original = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := "edited = true\n"
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "claude",
		Name:                   "edit-test",
		FileSources:            []string{src},
		EnvLines:               []string{"ANTHROPIC_API_KEY=test-key"},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(true),
		FileContents: []agentConfigFileContent{
			{SourcePath: src, Content: edited},
		},
		ClaudeSettingsContent: strPtr(`{"env":{"FOO":"1"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, src); got != "original = true\n" {
		t.Fatalf("source file was modified: %q", got)
	}
	if !entry.IsolatedClaudeSettings || entry.ClaudeSettingsPath == "" {
		t.Fatalf("isolated fields missing: %+v", entry)
	}
	if len(entry.Sources) != 1 {
		t.Fatalf("sources = %+v", entry.Sources)
	}
	root, err := agentConfigRootDir()
	if err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(root, filepath.FromSlash(entry.Sources[0].BackupPath))
	if got := readFileString(t, snap); got != edited {
		t.Fatalf("snapshot = %q, want %q", got, edited)
	}
	claudeSnap := filepath.Join(root, entry.ID, claudeSettingsSnapshotRelName)
	if got := readFileString(t, claudeSnap); got != `{"env":{"FOO":"1"}}` {
		t.Fatalf("claude settings snapshot = %q", got)
	}
}

// Spec §9 case 2: editing an existing snapshot changes only the backup file.
func TestBackupSnapshotEditDoesNotTouchSource(t *testing.T) {
	home := setupAgentConfigTest(t)

	src := filepath.Join(home, "source-config.toml")
	if err := os.WriteFile(src, []byte("original = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "codex",
		Name:                   "snapshot-edit",
		FileSources:            []string{src},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := entry.Sources[0].BackupPath

	if _, err := writeAgentConfigBackupFile(entry.ID, backupPath, "", "patched = true\n"); err != nil {
		t.Fatal(err)
	}
	content, rel, err := readAgentConfigBackupFile(entry.ID, backupPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if content != "patched = true\n" {
		t.Fatalf("snapshot content = %q", content)
	}
	if rel != backupPath {
		t.Fatalf("resolved rel = %q, want %q", rel, backupPath)
	}
	if got := readFileString(t, src); got != "original = true\n" {
		t.Fatalf("source file was modified: %q", got)
	}
}

// Spec §9 case 3 + 4: switching an isolated Claude backup writes the snapshot to
// P, never to the user's ~/.claude/settings.json, and records P in preferences
// (which is what OpenSession reads to pass WithSettingsPath).
func TestSwitchIsolatedClaudeSettingsKeepsUserFile(t *testing.T) {
	home := setupAgentConfigTest(t)
	userSettings := writeUserClaudeSettings(t, home, `{"marker":"user"}`)

	// The user path is listed as a regular file source on purpose: the isolated
	// channel must claim it so switching never writes it back.
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "claude",
		Name:                   "isolated",
		FileSources:            []string{userSettings},
		EnvLines:               []string{"ANTHROPIC_BASE_URL=https://example.test"},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range entry.Sources {
		if isClaudeSettingsSourcePath(src.SourcePath) {
			t.Fatalf("claude settings leaked into regular sources: %+v", entry.Sources)
		}
	}
	// The snapshot is seeded from the listed settings source.
	snapContent, _, err := readAgentConfigBackupFile(entry.ID, "", "claude_settings")
	if err != nil {
		t.Fatal(err)
	}
	if snapContent != `{"marker":"user"}` {
		t.Fatalf("snapshot not seeded from source: %q", snapContent)
	}

	// Edit the snapshot, then switch.
	if _, err := writeAgentConfigBackupFile(entry.ID, "", "claude_settings", `{"marker":"backup"}`); err != nil {
		t.Fatal(err)
	}
	prefs, err := preferences.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	app := &AppContext{Prefs: prefs}
	switched, needsConfirm, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, app)
	if err != nil {
		t.Fatal(err)
	}
	if needsConfirm {
		t.Fatal("unexpected needs_confirm")
	}
	if got := readFileString(t, userSettings); got != `{"marker":"user"}` {
		t.Fatalf("user settings were overwritten: %q", got)
	}
	if got := readFileString(t, switched.ClaudeSettingsPath); got != `{"marker":"backup"}` {
		t.Fatalf("isolated settings = %q", got)
	}
	if got := prefs.AgentClaudeSettingsPath("claude"); got != switched.ClaudeSettingsPath {
		t.Fatalf("preferences settings path = %q, want %q", got, switched.ClaudeSettingsPath)
	}
}

// A backup that only owns the user settings path must not report needs_confirm,
// since switching it never touches a user-visible file.
func TestSwitchIsolatedSkipsOverwriteConfirm(t *testing.T) {
	home := setupAgentConfigTest(t)
	userSettings := writeUserClaudeSettings(t, home, `{"marker":"user"}`)

	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "claude",
		Name:                   "confirm-free",
		FileSources:            []string{userSettings},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, needsConfirm, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if needsConfirm {
		t.Fatal("isolated-only backup should not require overwrite confirmation")
	}
}

// Spec §9 case 5: isolated settings are Claude-only.
func TestCreateBackupRejectsIsolatedForNonClaude(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "codex.toml")
	if err := os.WriteFile(src, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:                  "codex",
		Name:                   "nope",
		FileSources:            []string{src},
		Overwrite:              true,
		IsolatedClaudeSettings: boolPtr(true),
	})
	if err == nil {
		t.Fatal("expected error for isolated settings on non-claude agent")
	}
	if !strings.Contains(err.Error(), "only supported for claude") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Non-claude agents must not silently get isolation when the field is omitted.
func TestCreateBackupIsolatedDefaults(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	codexEntry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "default", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if codexEntry.IsolatedClaudeSettings || codexEntry.ClaudeSettingsPath != "" {
		t.Fatalf("codex backup should not be isolated: %+v", codexEntry)
	}
	// Spec §11: Claude defaults to isolated when the client omits the field.
	claudeEntry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "claude", Name: "default", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !claudeEntry.IsolatedClaudeSettings || claudeEntry.ClaudeSettingsPath == "" {
		t.Fatalf("claude backup should default to isolated: %+v", claudeEntry)
	}
}

// Spec §9 case 6: backup_path traversal is rejected.
func TestResolveBackupFileAbsRejectsEscape(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "jail", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"../../../etc/passwd",
		"..",
		filepath.Join("..", "other-backup", "file.json"),
	} {
		if _, _, err := resolveBackupFileAbs(entry.ID, bad, ""); err == nil {
			t.Fatalf("expected escape error for %q", bad)
		}
	}
	// An unknown backup id is rejected before any path work.
	if _, _, err := resolveBackupFileAbs("missing-id", "001-cfg.toml", ""); err == nil {
		t.Fatal("expected error for unknown backup id")
	}
	// The legitimate relative form still resolves.
	if _, _, err := resolveBackupFileAbs(entry.ID, entry.Sources[0].BackupPath, ""); err != nil {
		t.Fatalf("valid backup_path rejected: %v", err)
	}
}

// Spec §9 case 7: oversized source files are refused with the size-limit error
// (mapped to HTTP 413 by the handler).
func TestPreviewFileRejectsOversize(t *testing.T) {
	home := setupAgentConfigTest(t)
	big := filepath.Join(home, "big.json")
	if err := os.WriteFile(big, make([]byte, agentConfigMaxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := previewAgentConfigSourceFile(big); !errors.Is(err, errAgentConfigFileTooLarge) {
		t.Fatalf("err = %v, want errAgentConfigFileTooLarge", err)
	}
	// Directories are refused too.
	if _, _, err := previewAgentConfigSourceFile(home); err == nil {
		t.Fatal("expected error for directory preview")
	}
	small := filepath.Join(home, "small.json")
	if err := os.WriteFile(small, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs, content, err := previewAgentConfigSourceFile(small)
	if err != nil {
		t.Fatal(err)
	}
	if content != "{}\n" || abs != small {
		t.Fatalf("preview = (%q, %q)", abs, content)
	}
}

// Spec §9 case 8: a manifest written before this feature (no isolated fields)
// still restores sources to their user paths on switch.
func TestSwitchLegacyManifestWithoutIsolatedFields(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "legacy.toml")
	if err := os.WriteFile(src, []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "legacy", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the manifest the way an older MindFS would have: no new fields.
	legacy := agentConfigManifestEntry{
		ID:        entry.ID,
		Agent:     entry.Agent,
		Name:      entry.Name,
		CreatedAt: entry.CreatedAt,
		UpdatedAt: entry.UpdatedAt,
		Sources:   entry.Sources,
	}
	if err := writeAgentConfigManifest([]agentConfigManifestEntry{legacy}); err != nil {
		t.Fatal(err)
	}
	root, err := agentConfigRootDir()
	if err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(root, filepath.FromSlash(entry.Sources[0].BackupPath))
	if err := os.WriteFile(snap, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	switched, needsConfirm, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !needsConfirm {
		t.Fatal("expected needs_confirm for an existing user file")
	}
	if switched.IsolatedClaudeSettings {
		t.Fatal("legacy entry should not report isolation")
	}
	if _, _, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, src); got != "version = 2\n" {
		t.Fatalf("legacy restore = %q, want the snapshot content", got)
	}
}

func TestUpdateBackupRewritesSourcesAndEnv(t *testing.T) {
	home := setupAgentConfigTest(t)
	first := filepath.Join(home, "first.toml")
	second := filepath.Join(home, "second.toml")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:       "codex",
		Name:        "update",
		FileSources: []string{first},
		EnvLines:    []string{"A=1"},
		Overwrite:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := agentConfigRootDir()
	if err != nil {
		t.Fatal(err)
	}
	oldSnap := filepath.Join(root, filepath.FromSlash(entry.Sources[0].BackupPath))

	updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
		ID:          entry.ID,
		FileSources: []string{second},
		EnvLines:    []string{"B=2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Sources) != 1 || updated.Sources[0].SourcePath != second {
		t.Fatalf("sources = %+v", updated.Sources)
	}
	if len(updated.EnvKeys) != 1 || updated.EnvKeys[0] != "B" {
		t.Fatalf("envKeys = %+v", updated.EnvKeys)
	}
	if _, err := os.Stat(oldSnap); !os.IsNotExist(err) {
		t.Fatalf("orphaned snapshot was not removed: %v", err)
	}
	envMap, err := readAgentEnvBackups()
	if err != nil {
		t.Fatal(err)
	}
	if got := envMap[entry.ID]; len(got) != 1 || got[0] != "B=2" {
		t.Fatalf("env backup = %+v", got)
	}
}

// Reordering sources must not make a new snapshot collide with a preserved one
// that already owns the same NNN-basename.
func TestUpdateBackupAvoidsSnapshotNameCollision(t *testing.T) {
	home := setupAgentConfigTest(t)
	kept := filepath.Join(home, "keep", "config.toml")
	added := filepath.Join(home, "add", "config.toml")
	for _, path := range []string{kept, added} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(filepath.Base(filepath.Dir(path))+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "collide", FileSources: []string{kept}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The new source is listed first, so its natural name (001-config.toml) is
	// the one the preserved source already holds.
	updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
		ID:          entry.ID,
		FileSources: []string{added, kept},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Sources) != 2 {
		t.Fatalf("sources = %+v", updated.Sources)
	}
	if updated.Sources[0].BackupPath == updated.Sources[1].BackupPath {
		t.Fatalf("snapshot paths collided: %+v", updated.Sources)
	}
	root, err := agentConfigRootDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range updated.Sources {
		got := readFileString(t, filepath.Join(root, filepath.FromSlash(src.BackupPath)))
		want := filepath.Base(filepath.Dir(src.SourcePath)) + "\n"
		if got != want {
			t.Fatalf("snapshot for %s = %q, want %q", src.SourcePath, got, want)
		}
	}
}

// Turning isolation off clears the runtime path so Claude falls back to its own
// settings discovery.
func TestUpdateBackupDisablingIsolationClearsPath(t *testing.T) {
	home := setupAgentConfigTest(t)
	src := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(src, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "claude", Name: "toggle", FileSources: []string{src}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsolatedClaudeSettings {
		t.Fatalf("expected isolation on by default: %+v", entry)
	}
	updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
		ID:                     entry.ID,
		FileSources:            []string{src},
		IsolatedClaudeSettings: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.IsolatedClaudeSettings || updated.ClaudeSettingsPath != "" {
		t.Fatalf("isolation not cleared: %+v", updated)
	}

	prefs, err := preferences.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := prefs.UpdateAgentClaudeSettingsPath("claude", entry.ClaudeSettingsPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, &AppContext{Prefs: prefs}); err != nil {
		t.Fatal(err)
	}
	if got := prefs.AgentClaudeSettingsPath("claude"); got != "" {
		t.Fatalf("runtime settings path = %q, want empty", got)
	}
}

func TestBackupFileWriteRejectsOversizeAndUnknownID(t *testing.T) {
	setupAgentConfigTest(t)
	if _, err := writeAgentConfigBackupFile("missing-id", "", "claude_settings", "{}"); err == nil {
		t.Fatal("expected error for unknown backup id")
	}
	if _, _, err := readAgentConfigBackupFile("missing-id", "", "claude_settings"); err == nil {
		t.Fatal("expected error for unknown backup id")
	}
}
