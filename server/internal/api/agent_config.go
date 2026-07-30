package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mindfs/server/internal/agent"
	"mindfs/server/internal/apperr"
	configpkg "mindfs/server/internal/config"
	"mindfs/server/internal/preferences"
)

type agentConfigSource struct {
	SourcePath string `json:"sourcePath"`
	BackupPath string `json:"backupPath"`
}

type agentConfigManifestEntry struct {
	ID                     string              `json:"id"`
	Agent                  string              `json:"agent"`
	Name                   string              `json:"name"`
	CreatedAt              string              `json:"createdAt"`
	UpdatedAt              string              `json:"updatedAt"`
	Sources                []agentConfigSource `json:"sources,omitempty"`
	EnvKeys                []string            `json:"envKeys,omitempty"`
	IsolatedClaudeSettings bool                `json:"isolatedClaudeSettings,omitempty"`
	ClaudeSettingsPath     string              `json:"claudeSettingsPath,omitempty"`
}

type agentConfigFileContent struct {
	SourcePath string `json:"source_path"`
	Content    string `json:"content"`
}

type agentConfigBackupRequest struct {
	Agent                  string                   `json:"agent"`
	Name                   string                   `json:"name"`
	FileSources            []string                 `json:"file_sources"`
	EnvLines               []string                 `json:"env_lines"`
	Overwrite              bool                     `json:"overwrite"`
	IsolatedClaudeSettings *bool                    `json:"isolated_claude_settings,omitempty"`
	ClaudeSettingsPath     string                   `json:"claude_settings_path,omitempty"`
	FileContents           []agentConfigFileContent `json:"file_contents,omitempty"`
	ClaudeSettingsContent  *string                  `json:"claude_settings_content,omitempty"`
}

type agentConfigBackupUpdateRequest struct {
	ID                     string   `json:"id"`
	FileSources            []string `json:"file_sources"`
	EnvLines               []string `json:"env_lines"`
	IsolatedClaudeSettings *bool    `json:"isolated_claude_settings,omitempty"`
	ClaudeSettingsPath     string   `json:"claude_settings_path,omitempty"`
}

type agentConfigFileRequest struct {
	ID         string `json:"id"`
	BackupPath string `json:"backup_path"`
	Content    string `json:"content"`
	Kind       string `json:"kind"`
}

type agentConfigPreviewRequest struct {
	Path string `json:"path"`
}

type agentConfigSwitchRequest struct {
	ID               string `json:"id"`
	ConfirmOverwrite bool   `json:"confirm_overwrite"`
}

// agentConfigSwitchStep records one stage of a config switch for the UI. Only
// structured data is returned; the client maps Key to a localized label and
// fills Count/Target into it.
type agentConfigSwitchStep struct {
	Key        string `json:"key"`
	Status     string `json:"status"`
	Count      int    `json:"count,omitempty"`
	Target     string `json:"target,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type agentConfigSwitchResult struct {
	Entry        agentConfigManifestEntry `json:"backup"`
	NeedsConfirm bool                     `json:"needs_confirm"`
	Steps        []agentConfigSwitchStep  `json:"steps,omitempty"`
}

const (
	switchStepStatusOK      = "ok"
	switchStepStatusFailed  = "failed"
	switchStepStatusRunning = "running"
	switchStepStatusSkipped = "skipped"

	switchStepRestoreFiles    = "restore_files"
	switchStepClaudeSettings  = "claude_settings"
	switchStepApplyEnv        = "apply_env"
	switchStepKillSessions    = "kill_sessions"
	switchStepRecordSelection = "record_selection"
	switchStepProbe           = "probe"
)

// switchStepRecorder accumulates the step list as a switch progresses. A failed
// switch still returns everything recorded so far, so the UI can show which
// stage it stopped at.
type switchStepRecorder struct {
	steps []agentConfigSwitchStep
}

func (r *switchStepRecorder) ok(key string, start time.Time, count int, target string) {
	r.steps = append(r.steps, agentConfigSwitchStep{
		Key:        key,
		Status:     switchStepStatusOK,
		Count:      count,
		Target:     target,
		DurationMS: time.Since(start).Milliseconds(),
	})
}

func (r *switchStepRecorder) fail(key string, start time.Time, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	r.steps = append(r.steps, agentConfigSwitchStep{
		Key:        key,
		Status:     switchStepStatusFailed,
		DurationMS: time.Since(start).Milliseconds(),
		Error:      message,
	})
}

func (r *switchStepRecorder) skip(key string) {
	r.steps = append(r.steps, agentConfigSwitchStep{Key: key, Status: switchStepStatusSkipped})
}

func (r *switchStepRecorder) running(key string) {
	r.steps = append(r.steps, agentConfigSwitchStep{Key: key, Status: switchStepStatusRunning})
}

func (r *switchStepRecorder) result(entry agentConfigManifestEntry, needsConfirm bool) agentConfigSwitchResult {
	return agentConfigSwitchResult{Entry: entry, NeedsConfirm: needsConfirm, Steps: r.steps}
}

type agentRestartRequest struct {
	Agent string `json:"agent"`
}

const (
	agentConfigMaxFileBytes       = 1 << 20 // 1 MiB
	claudeSettingsSnapshotRelName = "claude-settings.json"
)

var agentConfigNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (h *HTTPHandler) handleAgentConfigDefaults(w http.ResponseWriter, r *http.Request) {
	agentName := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agentName == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("agent required"))
		return
	}
	cfg, err := agent.LoadConfig("")
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err)
		return
	}
	def, ok := cfg.GetAgent(agentName)
	if !ok {
		respondError(w, http.StatusNotFound, errInvalidRequest("agent not configured"))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"agent":        def.Name,
		"file_sources": existingDefaultFileSources(def.ConfigBackup.FileSources),
		"env_keys":     def.ConfigBackup.EnvKeys,
	})
}

func (h *HTTPHandler) handleAgentConfigBackupsList(w http.ResponseWriter, r *http.Request) {
	agentName := strings.TrimSpace(r.URL.Query().Get("agent"))
	manifest, err := readAgentConfigManifest()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err)
		return
	}
	if agentName == "" {
		respondJSON(w, http.StatusOK, manifest)
		return
	}
	filtered := make([]agentConfigManifestEntry, 0, len(manifest))
	for _, item := range manifest {
		if item.Agent == agentName {
			filtered = append(filtered, item)
		}
	}
	respondJSON(w, http.StatusOK, filtered)
}

func (h *HTTPHandler) handleAgentConfigBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req agentConfigBackupRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	entry, err := createAgentConfigBackup(req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errAgentConfigConflict) {
			status = http.StatusConflict
		}
		respondError(w, status, err)
		return
	}
	respondJSON(w, http.StatusOK, entry)
}

func (h *HTTPHandler) handleAgentConfigBackupUpdate(w http.ResponseWriter, r *http.Request) {
	var req agentConfigBackupUpdateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	entry, err := updateAgentConfigBackup(req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	respondJSON(w, http.StatusOK, entry)
}

func (h *HTTPHandler) handleAgentConfigBackupDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("backup id required"))
		return
	}
	var removedPath, removedAgent string
	if entry, err := findAgentConfigBackup(id); err == nil {
		removedPath = entry.ClaudeSettingsPath
		removedAgent = entry.Agent
	}
	manifest, err := deleteAgentConfigBackup(id)
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	if h.AppContext != nil && h.AppContext.GetPreferences() != nil && removedPath != "" && removedAgent != "" {
		prefs := h.AppContext.GetPreferences()
		if prefs.AgentClaudeSettingsPath(removedAgent) == removedPath {
			_ = prefs.UpdateAgentClaudeSettingsPath(removedAgent, "")
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id, "backups": manifest})
}

func (h *HTTPHandler) handleAgentConfigBackupFileGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	backupPath := strings.TrimSpace(r.URL.Query().Get("backup_path"))
	content, resolvedPath, err := readAgentConfigBackupFile(id, backupPath, kind)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errAgentConfigFileTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		respondError(w, status, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":          id,
		"backup_path": resolvedPath,
		"kind":        kind,
		"content":     content,
		"size":        len(content),
	})
}

func (h *HTTPHandler) handleAgentConfigBackupFilePut(w http.ResponseWriter, r *http.Request) {
	var req agentConfigFileRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	if int64(len(req.Content)) > agentConfigMaxFileBytes {
		respondError(w, http.StatusRequestEntityTooLarge, errAgentConfigFileTooLarge)
		return
	}
	resolvedPath, err := writeAgentConfigBackupFile(req.ID, req.BackupPath, req.Kind, req.Content)
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":          strings.TrimSpace(req.ID),
		"backup_path": resolvedPath,
		"kind":        strings.TrimSpace(req.Kind),
		"size":        len(req.Content),
	})
}

func (h *HTTPHandler) handleAgentConfigBackupEnvGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("backup id required"))
		return
	}
	if _, err := findAgentConfigBackup(id); err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	envMap, err := readAgentEnvBackups()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err)
		return
	}
	lines := envMap[id]
	if lines == nil {
		lines = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"env_lines": lines,
	})
}

func (h *HTTPHandler) handleAgentConfigPreviewFile(w http.ResponseWriter, r *http.Request) {
	var req agentConfigPreviewRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	abs, content, err := previewAgentConfigSourceFile(req.Path)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errAgentConfigFileTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		respondError(w, status, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"path":    abs,
		"content": content,
		"size":    len(content),
	})
}

func (h *HTTPHandler) handleAgentConfigSwitch(w http.ResponseWriter, r *http.Request) {
	var req agentConfigSwitchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	result, err := switchAgentConfig(req, h.AppContext)
	if err != nil {
		// Steps travel with the error so the client can show which stage failed
		// and warn that the config may be half-applied.
		respondErrorWithExtra(w, http.StatusBadRequest, err, map[string]any{"steps": result.Steps})
		return
	}
	if result.NeedsConfirm {
		respondJSON(w, http.StatusOK, map[string]any{
			"needs_confirm": true,
			"message":       "目标配置文件已存在，请确保已备份",
			"backup":        result.Entry,
		})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"needs_confirm": false,
		"backup":        result.Entry,
		"steps":         result.Steps,
	})
}

func (h *HTTPHandler) handleAgentRestart(w http.ResponseWriter, r *http.Request) {
	var req agentRestartRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxUploadRequestBytes)).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid request body"))
		return
	}
	if err := restartAgent(req.Agent, h.AppContext); err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"restarting": true,
		"agent":      strings.TrimSpace(req.Agent),
	})
}

var errAgentConfigConflict = errors.New("backup already exists")

func createAgentConfigBackup(req agentConfigBackupRequest) (agentConfigManifestEntry, error) {
	agentName, backupName, id, err := normalizeAgentConfigRequest(req.Agent, req.Name)
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	cfg, err := agent.LoadConfig("")
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	if _, ok := cfg.GetAgent(agentName); !ok {
		return agentConfigManifestEntry{}, fmt.Errorf("agent not configured: %s", agentName)
	}

	isolated := false
	if req.IsolatedClaudeSettings != nil {
		isolated = *req.IsolatedClaudeSettings
	} else if isClaudeAgentName(agentName) {
		// Spec default: isolated on for Claude.
		isolated = true
	}
	if isolated && !isClaudeAgentName(agentName) {
		return agentConfigManifestEntry{}, errors.New("isolated_claude_settings is only supported for claude agent")
	}

	contentBySource := map[string]string{}
	for _, item := range req.FileContents {
		source := strings.TrimSpace(item.SourcePath)
		if source == "" {
			continue
		}
		expanded, err := expandUserPath(source)
		if err != nil {
			return agentConfigManifestEntry{}, err
		}
		if int64(len(item.Content)) > agentConfigMaxFileBytes {
			return agentConfigManifestEntry{}, errAgentConfigFileTooLarge
		}
		contentBySource[expanded] = item.Content
	}

	configRoot, err := agentConfigRootDir()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	existingIndex := -1
	var createdAt string
	for index, item := range manifest {
		if item.ID == id {
			if !req.Overwrite {
				return agentConfigManifestEntry{}, errAgentConfigConflict
			}
			existingIndex = index
			createdAt = item.CreatedAt
			break
		}
	}
	now := time.Now().Format(time.RFC3339)
	if createdAt == "" {
		createdAt = now
	}
	entry := agentConfigManifestEntry{
		ID:        id,
		Agent:     agentName,
		Name:      backupName,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}

	// file_sources that only appear in file_contents still need to be snapshotted.
	fileSourcesInput := append([]string{}, req.FileSources...)
	for source := range contentBySource {
		fileSourcesInput = append(fileSourcesInput, source)
	}
	sources, err := normalizeFileSourcesAllowMissing(fileSourcesInput, contentBySource)
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	envLines, envKeys, err := normalizeEnvLines(req.EnvLines)
	if err != nil {
		return agentConfigManifestEntry{}, err
	}

	// The isolated channel owns ~/.claude/settings.json: keep it out of the regular
	// sources so switching never writes back to the user's Claude config.
	var claudeSettingsSources []string
	if isolated {
		sources, claudeSettingsSources = splitClaudeSettingsSources(sources)
	}

	var claudeSettingsContent string
	hasClaudeSettings := false
	if isolated {
		if req.ClaudeSettingsContent != nil {
			claudeSettingsContent = *req.ClaudeSettingsContent
			hasClaudeSettings = true
		}
		path, err := resolveClaudeSettingsPath(id, req.ClaudeSettingsPath)
		if err != nil {
			return agentConfigManifestEntry{}, err
		}
		entry.IsolatedClaudeSettings = true
		entry.ClaudeSettingsPath = path
		if !hasClaudeSettings {
			// Seed from a settings source the caller listed (edited content wins over disk).
			for _, source := range claudeSettingsSources {
				if content, ok := contentBySource[source]; ok {
					claudeSettingsContent = content
					hasClaudeSettings = true
					break
				}
				if data, err := os.ReadFile(source); err == nil && int64(len(data)) <= agentConfigMaxFileBytes {
					claudeSettingsContent = string(data)
					hasClaudeSettings = true
					break
				}
			}
		}
		if !hasClaudeSettings {
			// Best-effort seed from default user settings if present.
			if home, err := os.UserHomeDir(); err == nil {
				candidate := filepath.Join(home, ".claude", "settings.json")
				if data, err := os.ReadFile(candidate); err == nil {
					if int64(len(data)) <= agentConfigMaxFileBytes {
						claudeSettingsContent = string(data)
						hasClaudeSettings = true
					}
				}
			}
		}
		if !hasClaudeSettings {
			claudeSettingsContent = "{}\n"
			hasClaudeSettings = true
		}
		if int64(len(claudeSettingsContent)) > agentConfigMaxFileBytes {
			return agentConfigManifestEntry{}, errAgentConfigFileTooLarge
		}
	}

	if len(sources) == 0 && len(envLines) == 0 && !hasClaudeSettings {
		return agentConfigManifestEntry{}, errors.New("config source or environment variables required")
	}
	if err := os.RemoveAll(filepath.Join(configRoot, id)); err != nil {
		return agentConfigManifestEntry{}, apperr.Wrap("remove", filepath.Join(configRoot, id), err)
	}
	for index, source := range sources {
		name := fmt.Sprintf("%03d-%s", index+1, filepath.Base(source))
		rel := filepath.Join(id, name)
		dst := filepath.Join(configRoot, rel)
		if content, ok := contentBySource[source]; ok {
			if err := writeFileAtomic(dst, []byte(content), 0o600); err != nil {
				return agentConfigManifestEntry{}, err
			}
		} else if err := copyFile(source, dst); err != nil {
			return agentConfigManifestEntry{}, err
		}
		entry.Sources = append(entry.Sources, agentConfigSource{
			SourcePath: source,
			BackupPath: filepath.ToSlash(rel),
		})
	}
	if hasClaudeSettings {
		snap := filepath.Join(configRoot, id, claudeSettingsSnapshotRelName)
		if err := writeFileAtomic(snap, []byte(claudeSettingsContent), 0o600); err != nil {
			return agentConfigManifestEntry{}, err
		}
	}
	envMap, err := readAgentEnvBackups()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	if len(envLines) > 0 {
		envMap[id] = envLines
		entry.EnvKeys = envKeys
	} else {
		delete(envMap, id)
	}
	if err := writeAgentEnvBackups(envMap); err != nil {
		return agentConfigManifestEntry{}, err
	}
	if err := updateAgentConfigDefaults(agentName, sources, envKeys); err != nil {
		return agentConfigManifestEntry{}, err
	}
	if existingIndex >= 0 {
		manifest[existingIndex] = entry
	} else {
		manifest = append(manifest, entry)
	}
	if err := writeAgentConfigManifest(manifest); err != nil {
		return agentConfigManifestEntry{}, err
	}
	return entry, nil
}

func updateAgentConfigBackup(req agentConfigBackupUpdateRequest) (agentConfigManifestEntry, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return agentConfigManifestEntry{}, errors.New("backup id required")
	}
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	index := -1
	var entry agentConfigManifestEntry
	for i, item := range manifest {
		if item.ID == id {
			index = i
			entry = item
			break
		}
	}
	if index < 0 {
		return agentConfigManifestEntry{}, errors.New("backup not found")
	}

	isolated := entry.IsolatedClaudeSettings
	if req.IsolatedClaudeSettings != nil {
		isolated = *req.IsolatedClaudeSettings
	}
	if isolated && !isClaudeAgentName(entry.Agent) {
		return agentConfigManifestEntry{}, errors.New("isolated_claude_settings is only supported for claude agent")
	}

	configRoot, err := agentConfigRootDir()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	backupDir := filepath.Join(configRoot, id)

	// Preserve existing snapshot contents by sourcePath where possible.
	existingBySource := map[string]agentConfigSource{}
	for _, src := range entry.Sources {
		existingBySource[src.SourcePath] = src
	}

	sources, err := normalizeFileSources(req.FileSources)
	if err != nil {
		// Allow empty file sources when env or isolated settings remain.
		if len(req.FileSources) > 0 {
			return agentConfigManifestEntry{}, err
		}
		sources = nil
	}
	envLines, envKeys, err := normalizeEnvLines(req.EnvLines)
	if err != nil {
		return agentConfigManifestEntry{}, err
	}

	// With isolation on, ~/.claude/settings.json is owned by the isolated channel.
	var claudeSettingsSources []string
	if isolated {
		sources, claudeSettingsSources = splitClaudeSettingsSources(sources)
	}

	// Snapshot names of preserved sources are reserved so new ones never collide.
	reservedRel := map[string]bool{}
	for _, source := range sources {
		if prev, ok := existingBySource[source]; ok {
			reservedRel[prev.BackupPath] = true
		}
	}

	var newSources []agentConfigSource
	usedRel := map[string]bool{}
	for index, source := range sources {
		if prev, ok := existingBySource[source]; ok {
			// Keep existing snapshot file.
			newSources = append(newSources, prev)
			usedRel[prev.BackupPath] = true
			continue
		}
		base := filepath.Base(source)
		rel := filepath.ToSlash(filepath.Join(id, fmt.Sprintf("%03d-%s", index+1, base)))
		for seq := index + 1; reservedRel[rel] || usedRel[rel]; seq++ {
			rel = filepath.ToSlash(filepath.Join(id, fmt.Sprintf("%03d-%s", seq+1, base)))
		}
		dst := filepath.Join(configRoot, filepath.FromSlash(rel))
		if err := copyFile(source, dst); err != nil {
			return agentConfigManifestEntry{}, err
		}
		newSources = append(newSources, agentConfigSource{SourcePath: source, BackupPath: rel})
		usedRel[rel] = true
	}
	// Remove orphaned snapshot files (except claude-settings.json).
	if entries, err := os.ReadDir(backupDir); err == nil {
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			name := ent.Name()
			if name == claudeSettingsSnapshotRelName {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(id, name))
			if !usedRel[rel] {
				_ = os.Remove(filepath.Join(backupDir, name))
			}
		}
	}

	entry.Sources = newSources
	entry.UpdatedAt = time.Now().Format(time.RFC3339)
	entry.IsolatedClaudeSettings = isolated
	if isolated {
		path, err := resolveClaudeSettingsPath(id, firstNonEmpty(req.ClaudeSettingsPath, entry.ClaudeSettingsPath))
		if err != nil {
			return agentConfigManifestEntry{}, err
		}
		entry.ClaudeSettingsPath = path
		snap := filepath.Join(backupDir, claudeSettingsSnapshotRelName)
		if _, err := os.Stat(snap); os.IsNotExist(err) {
			// Seed from a settings path the caller listed, else start empty.
			seed := []byte("{}\n")
			for _, source := range claudeSettingsSources {
				if data, err := os.ReadFile(source); err == nil && int64(len(data)) <= agentConfigMaxFileBytes {
					seed = data
					break
				}
			}
			if err := writeFileAtomic(snap, seed, 0o600); err != nil {
				return agentConfigManifestEntry{}, err
			}
		}
	} else {
		entry.ClaudeSettingsPath = ""
	}

	if len(entry.Sources) == 0 && len(envLines) == 0 && !entry.IsolatedClaudeSettings {
		return agentConfigManifestEntry{}, errors.New("config source or environment variables required")
	}

	envMap, err := readAgentEnvBackups()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	if len(envLines) > 0 {
		envMap[id] = envLines
		entry.EnvKeys = envKeys
	} else {
		delete(envMap, id)
		entry.EnvKeys = nil
	}
	if err := writeAgentEnvBackups(envMap); err != nil {
		return agentConfigManifestEntry{}, err
	}
	if err := updateAgentConfigDefaults(entry.Agent, sources, envKeys); err != nil {
		return agentConfigManifestEntry{}, err
	}
	manifest[index] = entry
	if err := writeAgentConfigManifest(manifest); err != nil {
		return agentConfigManifestEntry{}, err
	}
	return entry, nil
}

func deleteAgentConfigBackup(id string) ([]agentConfigManifestEntry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("backup id required")
	}
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return nil, err
	}
	next := make([]agentConfigManifestEntry, 0, len(manifest))
	found := false
	var removed agentConfigManifestEntry
	for _, item := range manifest {
		if item.ID == id {
			found = true
			removed = item
			continue
		}
		next = append(next, item)
	}
	if !found {
		return nil, errors.New("backup not found")
	}
	if err := writeAgentConfigManifest(next); err != nil {
		return nil, err
	}
	configRoot, err := agentConfigRootDir()
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(filepath.Join(configRoot, id)); err != nil {
		return nil, apperr.Wrap("remove", filepath.Join(configRoot, id), err)
	}
	if removed.ClaudeSettingsPath != "" {
		_ = os.Remove(removed.ClaudeSettingsPath)
	}
	envBackups, err := readAgentEnvBackups()
	if err != nil {
		return nil, err
	}
	if _, ok := envBackups[id]; ok {
		delete(envBackups, id)
		if err := writeAgentEnvBackups(envBackups); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func switchAgentConfig(req agentConfigSwitchRequest, app *AppContext) (agentConfigSwitchResult, error) {
	rec := &switchStepRecorder{}
	var noEntry agentConfigManifestEntry

	id := strings.TrimSpace(req.ID)
	if id == "" {
		return rec.result(noEntry, false), errors.New("backup id required")
	}
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return rec.result(noEntry, false), err
	}
	var entry agentConfigManifestEntry
	for _, item := range manifest {
		if item.ID == id {
			entry = item
			break
		}
	}
	if entry.ID == "" {
		return rec.result(noEntry, false), errors.New("backup not found")
	}
	hasClaudeSnap := false
	if entry.IsolatedClaudeSettings {
		if root, err := agentConfigRootDir(); err == nil {
			if _, err := os.Stat(filepath.Join(root, entry.ID, claudeSettingsSnapshotRelName)); err == nil {
				hasClaudeSnap = true
			}
		}
	}
	if len(entry.Sources) == 0 && len(entry.EnvKeys) == 0 && !hasClaudeSnap {
		return rec.result(noEntry, false), errors.New("backup has no config content")
	}

	// Probing for existing targets happens before anything is written, so a
	// needs_confirm response carries no steps.
	restoreStart := time.Now()
	exists := false
	for _, source := range entry.Sources {
		if entry.IsolatedClaudeSettings && isClaudeSettingsSourcePath(source.SourcePath) {
			continue
		}
		sourcePath, err := expandUserPath(source.SourcePath)
		if err != nil {
			rec.fail(switchStepRestoreFiles, restoreStart, err)
			return rec.result(noEntry, false), err
		}
		if _, err := os.Stat(sourcePath); err == nil {
			exists = true
			break
		} else if err != nil && !os.IsNotExist(err) {
			wrapped := apperr.Wrap("stat", sourcePath, err)
			rec.fail(switchStepRestoreFiles, restoreStart, wrapped)
			return rec.result(noEntry, false), wrapped
		}
	}
	if exists && !req.ConfirmOverwrite {
		return agentConfigSwitchResult{Entry: entry, NeedsConfirm: true}, nil
	}
	configRoot, err := agentConfigRootDir()
	if err != nil {
		rec.fail(switchStepRestoreFiles, restoreStart, err)
		return rec.result(noEntry, false), err
	}
	restored := 0
	for _, source := range entry.Sources {
		// Legacy entries may still list ~/.claude/settings.json as a regular source.
		// With isolation on, that file belongs to the isolated channel below.
		if entry.IsolatedClaudeSettings && isClaudeSettingsSourcePath(source.SourcePath) {
			continue
		}
		sourcePath, err := expandUserPath(source.SourcePath)
		if err != nil {
			rec.fail(switchStepRestoreFiles, restoreStart, err)
			return rec.result(noEntry, false), err
		}
		if err := copyFile(filepath.Join(configRoot, filepath.FromSlash(source.BackupPath)), sourcePath); err != nil {
			rec.fail(switchStepRestoreFiles, restoreStart, err)
			return rec.result(noEntry, false), err
		}
		restored++
	}
	if restored > 0 {
		rec.ok(switchStepRestoreFiles, restoreStart, restored, "")
	} else {
		rec.skip(switchStepRestoreFiles)
	}

	// Isolated Claude settings: restore snapshot to P only (never user ~/.claude/settings.json).
	claudeStart := time.Now()
	if entry.IsolatedClaudeSettings {
		snap := filepath.Join(configRoot, entry.ID, claudeSettingsSnapshotRelName)
		p := strings.TrimSpace(entry.ClaudeSettingsPath)
		if p == "" {
			resolved, err := resolveClaudeSettingsPath(entry.ID, "")
			if err != nil {
				rec.fail(switchStepClaudeSettings, claudeStart, err)
				return rec.result(noEntry, false), err
			}
			p = resolved
		}
		if _, err := os.Stat(snap); err == nil {
			if err := copyFile(snap, p); err != nil {
				rec.fail(switchStepClaudeSettings, claudeStart, err)
				return rec.result(noEntry, false), err
			}
		}
		if app != nil && app.GetPreferences() != nil {
			if err := app.GetPreferences().UpdateAgentClaudeSettingsPath(entry.Agent, p); err != nil {
				rec.fail(switchStepClaudeSettings, claudeStart, err)
				return rec.result(noEntry, false), err
			}
		}
		applyRuntimeClaudeSettingsPath(app, entry.Agent, p)
		rec.ok(switchStepClaudeSettings, claudeStart, 0, p)
	} else {
		if isClaudeAgentName(entry.Agent) && app != nil && app.GetPreferences() != nil {
			_ = app.GetPreferences().UpdateAgentClaudeSettingsPath(entry.Agent, "")
			applyRuntimeClaudeSettingsPath(app, entry.Agent, "")
		}
		rec.skip(switchStepClaudeSettings)
	}

	envStart := time.Now()
	var env map[string]string
	if len(entry.EnvKeys) > 0 {
		envBackups, err := readAgentEnvBackups()
		if err != nil {
			rec.fail(switchStepApplyEnv, envStart, err)
			return rec.result(noEntry, false), err
		}
		lines, ok := envBackups[entry.ID]
		if !ok {
			err := errors.New("environment backup not found")
			rec.fail(switchStepApplyEnv, envStart, err)
			return rec.result(noEntry, false), err
		}
		parsedEnv, _, err := envLinesToMap(lines)
		if err != nil {
			rec.fail(switchStepApplyEnv, envStart, err)
			return rec.result(noEntry, false), err
		}
		env = parsedEnv
	}
	// Applied even when the backup has no env keys, so switching clears
	// variables left behind by the previous config.
	if err := updateAgentEnvConfig(entry.Agent, env); err != nil {
		rec.fail(switchStepApplyEnv, envStart, err)
		return rec.result(noEntry, false), err
	}
	if app != nil && app.GetAgentPool() != nil {
		if err := app.GetAgentPool().SetAgentEnv(entry.Agent, env); err != nil {
			rec.fail(switchStepApplyEnv, envStart, err)
			return rec.result(noEntry, false), err
		}
	}
	if app != nil && app.GetProber() != nil {
		if err := app.GetProber().SetAgentEnv(entry.Agent, env); err != nil {
			rec.fail(switchStepApplyEnv, envStart, err)
			return rec.result(noEntry, false), err
		}
	}
	rec.ok(switchStepApplyEnv, envStart, len(env), "")

	killStart := time.Now()
	if app != nil && app.GetAgentPool() != nil {
		app.GetAgentPool().KillAgentProcess(entry.Agent, 0)
		rec.ok(switchStepKillSessions, killStart, 0, entry.Agent)
	} else {
		rec.skip(switchStepKillSessions)
	}

	recordStart := time.Now()
	if app != nil && app.GetPreferences() != nil {
		if err := app.GetPreferences().UpdateAgentLastConfigSelection(entry.Agent, preferences.LastConfigSelection{
			Type: "backup",
			ID:   entry.ID,
			Name: entry.Name,
		}); err != nil {
			rec.fail(switchStepRecordSelection, recordStart, err)
			return rec.result(noEntry, false), err
		}
		rec.ok(switchStepRecordSelection, recordStart, 0, entry.Name)
	} else {
		rec.skip(switchStepRecordSelection)
	}

	triggerAgentConfigSwitchProbe(app, entry.Agent, entry.ID, entry.Name)
	// The probe runs in the background; its completion arrives over WS as
	// agent.config.switched.
	rec.running(switchStepProbe)
	return rec.result(entry, false), nil
}

func restartAgent(agentName string, app *AppContext) error {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return errors.New("agent required")
	}
	if app == nil || app.GetAgentPool() == nil {
		return errors.New("agent pool not configured")
	}
	if _, ok := app.GetAgentPool().Config().GetAgent(agentName); !ok {
		return fmt.Errorf("agent not configured: %s", agentName)
	}
	app.GetAgentPool().KillAgentProcess(agentName, 0)
	triggerAgentConfigSwitchProbe(app, agentName, "", "")
	return nil
}

// applyRuntimeClaudeSettingsPath pushes the isolated settings path into the
// live pool and prober. The pool covers real sessions; the prober covers the
// probe sessions that populate the agent's model list.
func applyRuntimeClaudeSettingsPath(app *AppContext, agentName, settingsPath string) {
	if app == nil {
		return
	}
	if pool := app.GetAgentPool(); pool != nil {
		if err := pool.SetAgentClaudeSettingsPath(agentName, settingsPath); err != nil {
			log.Printf("[agent-config] pool_settings_path.error agent=%s err=%v", agentName, err)
		}
	}
	if prober := app.GetProber(); prober != nil {
		if err := prober.SetAgentClaudeSettingsPath(agentName, settingsPath); err != nil {
			log.Printf("[agent-config] prober_settings_path.error agent=%s err=%v", agentName, err)
		}
	}
}

// agentConfigSwitchedEvent reports the outcome of the background probe that
// follows a config switch, an API provider switch or a manual restart.
type agentConfigSwitchedEvent struct {
	Agent      string `json:"agent"`
	BackupID   string `json:"backup_id,omitempty"`
	BackupName string `json:"backup_name,omitempty"`
	Available  bool   `json:"available"`
	Error      string `json:"error,omitempty"`
}

// agentConfigSwitchNotifier exists so tests can observe the completion
// broadcast. A missing broadcast is the worst failure mode here: the server log
// looks healthy while the client waits on the probe forever.
type agentConfigSwitchNotifier interface {
	AgentConfigSwitched(evt agentConfigSwitchedEvent)
}

// switchProbeNotifier is overridden in tests; nil means "use the AppContext".
var switchProbeNotifier agentConfigSwitchNotifier

func resolveSwitchNotifier(app *AppContext) agentConfigSwitchNotifier {
	if switchProbeNotifier != nil {
		return switchProbeNotifier
	}
	if app == nil {
		return nil
	}
	return app
}

func triggerAgentConfigSwitchProbe(app *AppContext, agentName, backupID, backupName string) {
	if app == nil || app.GetProber() == nil {
		return
	}
	prober := app.GetProber()
	notifier := resolveSwitchNotifier(app)
	if err := prober.ClearProbeSession(agentName); err != nil {
		log.Printf("[agent-config] clear_probe_session.error agent=%s err=%v", agentName, err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		status := prober.ProbeOne(ctx, agentName)
		probeErr := firstNonEmpty(status.Error, status.ProbeError, status.RuntimeError)
		if probeErr != "" {
			log.Printf("[agent-config] switch_probe.completed agent=%s available=%t err=%q", agentName, status.Available, probeErr)
		} else {
			log.Printf("[agent-config] switch_probe.completed agent=%s available=%t", agentName, status.Available)
		}
		// Broadcast unconditionally. agent.status.changed is filtered by
		// Prober.statusChanged and is silently dropped when nothing about the
		// status differs, which happens when switching between two backups that
		// both work and expose the same models.
		if notifier != nil {
			notifier.AgentConfigSwitched(agentConfigSwitchedEvent{
				Agent:      agentName,
				BackupID:   backupID,
				BackupName: backupName,
				Available:  status.Available,
				Error:      probeErr,
			})
		}
	}()
}

func normalizeAgentConfigRequest(agentName, backupName string) (string, string, string, error) {
	agentName = strings.TrimSpace(agentName)
	backupName = strings.TrimSpace(backupName)
	if agentName == "" {
		return "", "", "", errors.New("agent required")
	}
	if backupName == "" {
		return "", "", "", errors.New("backup name required")
	}
	if !agentConfigNamePattern.MatchString(backupName) || strings.Contains(backupName, "..") {
		return "", "", "", errors.New("backup name may only contain letters, numbers, dot, underscore, and hyphen")
	}
	return agentName, backupName, agentName + "-" + backupName, nil
}

func normalizeFileSources(input []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range input {
		path := strings.TrimSpace(item)
		if path == "" {
			continue
		}
		path, err := expandUserPath(path)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, apperr.Wrap("stat", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("config source is a directory: %s", path)
		}
		seen[path] = true
		out = append(out, path)
	}
	return out, nil
}

func existingDefaultFileSources(input []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range input {
		path := strings.TrimSpace(item)
		if path == "" {
			continue
		}
		path, err := expandUserPath(path)
		if err != nil || path == "" || seen[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func normalizeEnvLines(input []string) ([]string, []string, error) {
	var lines []string
	var keys []string
	seen := map[string]bool{}
	for _, raw := range input {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		rawKey, rawValue, ok := strings.Cut(line, "=")
		key := strings.TrimSpace(rawKey)
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("invalid env line: %s", line)
		}
		value := strings.TrimSpace(rawValue)
		if value == "" {
			continue
		}
		lines = append(lines, key+"="+value)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return lines, keys, nil
}

func envLinesToMap(lines []string) (map[string]string, []string, error) {
	env := make(map[string]string, len(lines))
	var keys []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("invalid env line: %s", line)
		}
		env[key] = value
		keys = append(keys, key)
	}
	return env, keys, nil
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimLeft(path[1:], `/\`)), nil
}

func updateAgentConfigDefaults(agentName string, fileSources []string, envKeys []string) error {
	path, err := agent.ResolveConfigPath()
	if err != nil {
		return err
	}
	cfg, err := agent.LoadConfig("")
	if err != nil {
		return err
	}
	found := false
	for i := range cfg.Agents {
		if cfg.Agents[i].Name != agentName {
			continue
		}
		found = true
		cfg.Agents[i].ConfigBackup.FileSources = append([]string(nil), fileSources...)
		cfg.Agents[i].ConfigBackup.EnvKeys = append([]string(nil), envKeys...)
		break
	}
	if !found {
		return fmt.Errorf("agent not configured: %s", agentName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(path), err)
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return apperr.Wrap("write", path, os.WriteFile(path, payload, 0o644))
}

func updateAgentEnvConfig(agentName string, env map[string]string) error {
	path, err := agent.ResolveConfigPath()
	if err != nil {
		return err
	}
	cfg, err := agent.LoadConfig("")
	if err != nil {
		return err
	}
	found := false
	for i := range cfg.Agents {
		if cfg.Agents[i].Name != agentName {
			continue
		}
		found = true
		cfg.Agents[i].Env = cloneStringMap(env)
		break
	}
	if !found {
		return fmt.Errorf("agent not configured: %s", agentName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(path), err)
	}
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return apperr.Wrap("write", path, os.WriteFile(path, payload, 0o644))
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func readAgentConfigManifest() ([]agentConfigManifestEntry, error) {
	path, err := agentConfigManifestPath()
	if err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []agentConfigManifestEntry{}, nil
		}
		return nil, apperr.Wrap("read", path, err)
	}
	var manifest []agentConfigManifestEntry
	if len(strings.TrimSpace(string(payload))) == 0 {
		return []agentConfigManifestEntry{}, nil
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func writeAgentConfigManifest(manifest []agentConfigManifestEntry) error {
	path, err := agentConfigManifestPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(path), err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return apperr.Wrap("write", path, os.WriteFile(path, payload, 0o644))
}

func readAgentEnvBackups() (map[string][]string, error) {
	path, err := agentEnvPath()
	if err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, apperr.Wrap("read", path, err)
	}
	if len(strings.TrimSpace(string(payload))) == 0 {
		return map[string][]string{}, nil
	}
	var out map[string][]string
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string][]string{}
	}
	return out, nil
}

func writeAgentEnvBackups(env map[string][]string) error {
	path, err := agentEnvPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(path), err)
	}
	payload, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return apperr.Wrap("write", path, os.WriteFile(path, payload, 0o644))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return apperr.Wrap("open", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return apperr.Wrap("write", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return apperr.Wrap("copy", dst, err)
	}
	return apperr.Wrap("write", dst, out.Close())
}

func agentConfigRootDir() (string, error) {
	configDir, err := configpkg.MindFSConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "agents-config"), nil
}

func agentConfigManifestPath() (string, error) {
	root, err := agentConfigRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "manifest.json"), nil
}

func agentEnvPath() (string, error) {
	configDir, err := configpkg.MindFSConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "agents-env.json"), nil
}

var errAgentConfigFileTooLarge = errors.New("config file exceeds size limit")

func isClaudeAgentName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "claudecode", "claude-code":
		return true
	default:
		return false
	}
}

// isClaudeSettingsSourcePath reports whether a config source path points at a
// Claude settings.json under a .claude directory. When a backup uses isolated
// Claude settings, such a path is owned by the isolated channel and must never
// be restored back onto the user's file.
func isClaudeSettingsSourcePath(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return false
	}
	if !strings.EqualFold(filepath.Base(clean), "settings.json") {
		return false
	}
	return strings.EqualFold(filepath.Base(filepath.Dir(clean)), ".claude")
}

// splitClaudeSettingsSources partitions sources into regular ones and Claude
// settings ones, preserving order.
func splitClaudeSettingsSources(sources []string) (regular []string, claudeSettings []string) {
	for _, source := range sources {
		if isClaudeSettingsSourcePath(source) {
			claudeSettings = append(claudeSettings, source)
			continue
		}
		regular = append(regular, source)
	}
	return regular, claudeSettings
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func claudeSettingsRootDir() (string, error) {
	configDir, err := configpkg.MindFSConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "claude-settings"), nil
}

func resolveClaudeSettingsPath(backupID, requested string) (string, error) {
	root, err := claudeSettingsRootDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", apperr.Wrap("mkdir", root, err)
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return filepath.Join(root, backupID+".json"), nil
	}
	abs, err := expandUserPath(requested)
	if err != nil {
		return "", err
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("claude_settings_path must be under %s", root)
	}
	return abs, nil
}

// normalizeFileSourcesAllowMissing is like normalizeFileSources but allows paths
// that only exist as in-memory file_contents (not yet on disk).
func normalizeFileSourcesAllowMissing(input []string, contentBySource map[string]string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, item := range input {
		path := strings.TrimSpace(item)
		if path == "" {
			continue
		}
		path, err := expandUserPath(path)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		if _, hasContent := contentBySource[path]; hasContent {
			seen[path] = true
			out = append(out, path)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, apperr.Wrap("stat", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("config source is a directory: %s", path)
		}
		seen[path] = true
		out = append(out, path)
	}
	return out, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap("mkdir", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return apperr.Wrap("write", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if retryErr := os.Rename(tmp, path); retryErr != nil {
			return apperr.Wrap("rename", path, err)
		}
	}
	return nil
}

func findAgentConfigBackup(id string) (agentConfigManifestEntry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return agentConfigManifestEntry{}, errors.New("backup id required")
	}
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return agentConfigManifestEntry{}, err
	}
	for _, item := range manifest {
		if item.ID == id {
			return item, nil
		}
	}
	return agentConfigManifestEntry{}, errors.New("backup not found")
}

func resolveBackupFileAbs(id, backupPath, kind string) (abs string, rel string, err error) {
	id = strings.TrimSpace(id)
	kind = strings.TrimSpace(kind)
	backupPath = strings.TrimSpace(backupPath)
	if id == "" {
		return "", "", errors.New("backup id required")
	}
	if _, err := findAgentConfigBackup(id); err != nil {
		return "", "", err
	}
	configRoot, err := agentConfigRootDir()
	if err != nil {
		return "", "", err
	}
	if kind == "claude_settings" {
		rel = filepath.ToSlash(filepath.Join(id, claudeSettingsSnapshotRelName))
		abs = filepath.Join(configRoot, id, claudeSettingsSnapshotRelName)
		return abs, rel, nil
	}
	if backupPath == "" {
		return "", "", errors.New("backup_path required")
	}
	// Accept either "id/file" or "file" relative forms.
	clean := filepath.Clean(filepath.FromSlash(backupPath))
	if !strings.HasPrefix(clean, id+string(os.PathSeparator)) && clean != id {
		clean = filepath.Join(id, clean)
	}
	abs = filepath.Join(configRoot, clean)
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", "", err
	}
	rootAbs, err := filepath.Abs(filepath.Join(configRoot, id))
	if err != nil {
		return "", "", err
	}
	relToID, err := filepath.Rel(rootAbs, abs)
	if err != nil || strings.HasPrefix(relToID, "..") {
		return "", "", errors.New("backup_path escapes backup directory")
	}
	return abs, filepath.ToSlash(filepath.Join(id, relToID)), nil
}

func readAgentConfigBackupFile(id, backupPath, kind string) (content string, rel string, err error) {
	abs, rel, err := resolveBackupFileAbs(id, backupPath, kind)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", apperr.Wrap("stat", abs, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("not a file: %s", abs)
	}
	if info.Size() > agentConfigMaxFileBytes {
		return "", "", errAgentConfigFileTooLarge
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", "", apperr.Wrap("read", abs, err)
	}
	return string(data), rel, nil
}

func writeAgentConfigBackupFile(id, backupPath, kind, content string) (rel string, err error) {
	abs, rel, err := resolveBackupFileAbs(id, backupPath, kind)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(abs, []byte(content), 0o600); err != nil {
		return "", err
	}
	// Touch manifest updatedAt.
	manifest, err := readAgentConfigManifest()
	if err != nil {
		return rel, nil
	}
	for i := range manifest {
		if manifest[i].ID == strings.TrimSpace(id) {
			manifest[i].UpdatedAt = time.Now().Format(time.RFC3339)
			_ = writeAgentConfigManifest(manifest)
			break
		}
	}
	return rel, nil
}

func previewAgentConfigSourceFile(path string) (abs string, content string, err error) {
	abs, err = expandUserPath(path)
	if err != nil {
		return "", "", err
	}
	if abs == "" {
		return "", "", errors.New("path required")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", apperr.Wrap("stat", abs, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("path is a directory: %s", abs)
	}
	if info.Size() > agentConfigMaxFileBytes {
		return "", "", errAgentConfigFileTooLarge
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", "", apperr.Wrap("read", abs, err)
	}
	return abs, string(data), nil
}
