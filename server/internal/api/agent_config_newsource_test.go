package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What happens when the edit form is given a brand-new source path.
func TestUpdateBackupWithNewSourcePath(t *testing.T) {
	home := setupAgentConfigTest(t)
	original := filepath.Join(home, "original.toml")
	if err := os.WriteFile(original, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "newsrc", FileSources: []string{original}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := agentConfigRootDir()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("路径不存在则整个保存失败", func(t *testing.T) {
		missing := filepath.Join(home, "does-not-exist.toml")
		_, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
			ID:          entry.ID,
			FileSources: []string{original, missing},
		})
		if err == nil {
			t.Fatal("expected error for a source path that is not on disk")
		}
		t.Logf("错误信息: %v", err)
		// 原有条目必须保持不变（不能被部分写入破坏）
		after, err := findAgentConfigBackup(entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Sources) != 1 || after.Sources[0].SourcePath != original {
			t.Fatalf("失败的更新破坏了原有 sources: %+v", after.Sources)
		}
		if got := readFileString(t, filepath.Join(root, filepath.FromSlash(after.Sources[0].BackupPath))); got != "original\n" {
			t.Fatalf("原快照被破坏: %q", got)
		}
	})

	t.Run("路径存在则从磁盘拷入新快照", func(t *testing.T) {
		added := filepath.Join(home, "added.toml")
		if err := os.WriteFile(added, []byte("added-from-disk\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
			ID:          entry.ID,
			FileSources: []string{original, added},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(updated.Sources) != 2 {
			t.Fatalf("sources = %+v", updated.Sources)
		}
		var addedSnap string
		for _, s := range updated.Sources {
			if s.SourcePath == added {
				addedSnap = s.BackupPath
			}
		}
		if addedSnap == "" {
			t.Fatalf("新路径没有进入 sources: %+v", updated.Sources)
		}
		if got := readFileString(t, filepath.Join(root, filepath.FromSlash(addedSnap))); got != "added-from-disk\n" {
			t.Fatalf("新快照内容 = %q，应为磁盘当前内容", got)
		}
	})

	t.Run("移除路径会删掉对应快照", func(t *testing.T) {
		before, err := findAgentConfigBackup(entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		var dropped string
		for _, s := range before.Sources {
			if s.SourcePath == original {
				dropped = s.BackupPath
			}
		}
		updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
			ID:          entry.ID,
			FileSources: []string{filepath.Join(home, "added.toml")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(updated.Sources) != 1 {
			t.Fatalf("sources = %+v", updated.Sources)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dropped))); !os.IsNotExist(err) {
			t.Fatalf("被移除来源的快照仍存在: %v", err)
		}
	})
}

// Adding the user's Claude settings path to an isolated backup.
func TestUpdateBackupNewClaudeSettingsSourceIsClaimed(t *testing.T) {
	home := setupAgentConfigTest(t)
	cfg := filepath.Join(home, "cfg.toml")
	if err := os.WriteFile(cfg, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userSettings := writeUserClaudeSettings(t, home, `{"marker":"user"}`)
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "claude", Name: "claim", FileSources: []string{cfg}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsolatedClaudeSettings {
		t.Fatal("expected isolation on by default")
	}
	updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
		ID:          entry.ID,
		FileSources: []string{cfg, userSettings},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range updated.Sources {
		if isClaudeSettingsSourcePath(s.SourcePath) {
			t.Fatalf("isolated 备份仍把 claude settings 收进了普通 sources: %+v", updated.Sources)
		}
	}
	if len(updated.Sources) != 1 {
		t.Fatalf("sources = %+v", updated.Sources)
	}
	// 切换时不得回写用户文件
	if _, err := switchAgentConfig(agentConfigSwitchRequest{ID: entry.ID, ConfirmOverwrite: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, userSettings); got != `{"marker":"user"}` {
		t.Fatalf("用户 settings 被覆盖: %q", got)
	}
}

// A path that expands (~) or has different spelling should not silently duplicate.
func TestUpdateBackupNormalizesNewSourcePath(t *testing.T) {
	home := setupAgentConfigTest(t)
	cfg := filepath.Join(home, "dup.toml")
	if err := os.WriteFile(cfg, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := createAgentConfigBackup(agentConfigBackupRequest{
		Agent: "codex", Name: "dup", FileSources: []string{cfg}, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updateAgentConfigBackup(agentConfigBackupUpdateRequest{
		ID:          entry.ID,
		FileSources: []string{cfg, "~/dup.toml", "  " + cfg + "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Sources) != 1 {
		t.Fatalf("同一路径的不同写法应去重，实际: %+v", updated.Sources)
	}
	if !strings.EqualFold(updated.Sources[0].SourcePath, cfg) {
		t.Fatalf("sourcePath = %q", updated.Sources[0].SourcePath)
	}
}
