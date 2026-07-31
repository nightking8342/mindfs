package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mindfs/server/internal/agent"
)

// isolateAgentConfigDir points MindFSConfigDir at a temp directory on both
// POSIX (XDG_CONFIG_HOME) and Windows (%AppData%), and registers a codex agent
// so createAgentConfigBackup accepts requests for it. Missing the Windows half
// makes these tests rewrite the real user's backups.
func isolateAgentConfigDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	configPath := filepath.Join(home, "agents.json")
	t.Setenv("MINDFS_AGENTS_CONFIG", configPath)
	writeJSON(t, configPath, agent.Config{
		Agents: []agent.Definition{
			{Name: "codex", Command: "codex", Protocol: agent.ProtocolCodexSDK},
		},
	})
	return home
}

// truncateAgentConfigManifest reproduces the state left behind when a manifest
// write is cut short: the file exists but holds no bytes.
func truncateAgentConfigManifest(t *testing.T) string {
	t.Helper()
	path, err := agentConfigManifestPath()
	if err != nil {
		t.Fatalf("agentConfigManifestPath: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("truncate manifest: %v", err)
	}
	return path
}

// A truncated manifest used to read back as "no backups", so the next create
// persisted a manifest holding only the new entry and dropped every recorded
// backup while its snapshot stayed on disk.
func TestCreateAgentConfigBackupRejectsTruncatedManifest(t *testing.T) {
	home := isolateAgentConfigDir(t)

	source := filepath.Join(home, "config.toml")
	if err := os.WriteFile(source, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	for _, name := range []string{"jun", "cpan2"} {
		if _, err := createAgentConfigBackup(agentConfigBackupRequest{
			Agent:       "codex",
			Name:        name,
			FileSources: []string{source},
		}); err != nil {
			t.Fatalf("createAgentConfigBackup %s: %v", name, err)
		}
	}

	manifestPath := truncateAgentConfigManifest(t)

	if _, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent:       "codex",
		Name:        "cpa",
		FileSources: []string{source},
	}); err == nil {
		t.Fatal("createAgentConfigBackup succeeded on a truncated manifest, which overwrites recorded backups")
	} else if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncated-manifest error, got: %v", err)
	}

	// The manifest must be left untouched so the recorded backups stay
	// recoverable instead of being replaced by the single new entry.
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(payload) != 0 {
		t.Fatalf("manifest was rewritten after the failed create: %q", payload)
	}

	configRoot, err := agentConfigRootDir()
	if err != nil {
		t.Fatalf("agentConfigRootDir: %v", err)
	}
	for _, id := range []string{"codex-jun", "codex-cpan2"} {
		if _, err := os.Stat(filepath.Join(configRoot, id, "001-config.toml")); err != nil {
			t.Fatalf("snapshot for %s went missing: %v", id, err)
		}
	}
}

func TestReadAgentConfigManifestRejectsEmptyFile(t *testing.T) {
	isolateAgentConfigDir(t)
	if err := writeAgentConfigManifest([]agentConfigManifestEntry{}); err != nil {
		t.Fatalf("writeAgentConfigManifest: %v", err)
	}

	// An empty manifest list is a legitimate state and must still read back as
	// such — only a zero-byte file signals a truncated write.
	entries, err := readAgentConfigManifest()
	if err != nil {
		t.Fatalf("readAgentConfigManifest on empty list: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}

	truncateAgentConfigManifest(t)
	if _, err := readAgentConfigManifest(); err == nil {
		t.Fatal("readAgentConfigManifest accepted a zero-byte manifest")
	}
}

func TestReadAgentEnvBackupsRejectsEmptyFile(t *testing.T) {
	isolateAgentConfigDir(t)
	if err := writeAgentEnvBackups(map[string][]string{}); err != nil {
		t.Fatalf("writeAgentEnvBackups: %v", err)
	}
	if _, err := readAgentEnvBackups(); err != nil {
		t.Fatalf("readAgentEnvBackups on empty map: %v", err)
	}

	path, err := agentEnvPath()
	if err != nil {
		t.Fatalf("agentEnvPath: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("truncate env backups: %v", err)
	}
	if _, err := readAgentEnvBackups(); err == nil {
		t.Fatal("readAgentEnvBackups accepted a zero-byte file")
	}
}

// Both files are written through the same temp-file-and-rename path as the
// snapshots, so an interrupted write can no longer leave a truncated file in
// place of the real one.
func TestAgentConfigStateFilesAreWrittenAtomically(t *testing.T) {
	isolateAgentConfigDir(t)

	entries := []agentConfigManifestEntry{{ID: "codex-jun", Agent: "codex", Name: "jun"}}
	if err := writeAgentConfigManifest(entries); err != nil {
		t.Fatalf("writeAgentConfigManifest: %v", err)
	}
	if err := writeAgentEnvBackups(map[string][]string{"codex-jun": {"KEY=value"}}); err != nil {
		t.Fatalf("writeAgentEnvBackups: %v", err)
	}

	manifestPath, err := agentConfigManifestPath()
	if err != nil {
		t.Fatalf("agentConfigManifestPath: %v", err)
	}
	envPath, err := agentEnvPath()
	if err != nil {
		t.Fatalf("agentEnvPath: %v", err)
	}
	for _, path := range []string{manifestPath, envPath} {
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("temp file left behind next to %s", path)
		}
	}

	got, err := readAgentConfigManifest()
	if err != nil {
		t.Fatalf("readAgentConfigManifest: %v", err)
	}
	if len(got) != 1 || got[0].ID != "codex-jun" {
		t.Fatalf("manifest round-trip mismatch: %+v", got)
	}
	env, err := readAgentEnvBackups()
	if err != nil {
		t.Fatalf("readAgentEnvBackups: %v", err)
	}
	if len(env["codex-jun"]) != 1 || env["codex-jun"][0] != "KEY=value" {
		t.Fatalf("env round-trip mismatch: %+v", env)
	}
}
