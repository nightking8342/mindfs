package fs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	fileChangeBatchDelay = time.Second
	maxWatchDirs         = 1024
)

var ignoredWatchNames = map[string]struct{}{
	".DS_Store":     {},
	".cache":        {},
	".git":          {},
	".gradle":       {},
	".mindfs":       {},
	".mypy_cache":   {},
	".next":         {},
	".nuxt":         {},
	".output":       {},
	".parcel-cache": {},
	".pytest_cache": {},
	".ruff_cache":   {},
	".svelte-kit":   {},
	".turbo":        {},
	".venv":         {},
	".vercel":       {},
	".vite":         {},
	"__pycache__":   {},
	"build":         {},
	"coverage":      {},
	"dist":          {},
	"node_modules":  {},
	"target":        {},
	"tmp":           {},
	"vendor":        {},
	"venv":          {},
}

// SharedFileWatcher manages file watching for one root shared by multiple sessions.
type SharedFileWatcher struct {
	root         RootInfo
	watcher      *fsnotify.Watcher
	sessionStore SessionFileRecorder

	mu                sync.RWMutex
	sessions          map[string]*sessionInfo
	watchedDirs       map[string]struct{}
	pendingWrites     map[string]string
	pendingChanges    map[string]FileChangeEvent
	pendingChangeDirs map[string]struct{}
	fileChangeTimer   *time.Timer
	fileChangeVersion uint64
	onFileChange      func(FileChangeEvent)
	onFileChangeBatch func(FileChangeBatchEvent)
	onRelatedFile     func(RelatedFileEvent)
	worktreeResolver  WorktreeResolver

	done chan struct{}
	// watchQueue hands directory-watch requests to a dedicated goroutine.
	// fsnotify.Add blocks until the reader goroutine replies, and that reader
	// writes into watcher.Events — so calling Add from run() (the only
	// consumer of Events) deadlocks. WatchDir must never run on run().
	watchQueue chan string
}

type SessionFileRecorder interface {
	RecordOutputFile(ctx context.Context, key, path string) error
	RecordOutputFileAtHead(ctx context.Context, key, path, head string) error
	RecordOutputFileInRepo(ctx context.Context, key, rootID, repoKind, repoPath, repoName, path, head string) error
	RecordRelatedWorktree(ctx context.Context, key, rootID, path, branch, head string) (bool, error)
}

type sessionInfo struct {
	key string
}

type FileChangeEvent struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	IsDir  bool   `json:"is_dir"`
}

type FileChangeBatchEvent struct {
	RootID string            `json:"root_id"`
	Paths  []string          `json:"paths"`
	Dirs   []string          `json:"dirs"`
	Events []FileChangeEvent `json:"events"`
}

type RelatedFileEvent struct {
	RootID          string                `json:"root_id"`
	SessionKey      string                `json:"session_key"`
	Path            string                `json:"path"`
	RelatedWorktree *RelatedWorktreeMatch `json:"related_worktree,omitempty"`
}

type RelatedWorktreeMatch struct {
	Path    string
	Branch  string
	Head    string
	Current bool
}

type relatedFileRecord struct {
	rootID   string
	repoKind string
	repoPath string
	repoName string
	path     string
	head     string
}

type WorktreeResolver func(ctx context.Context, root RootInfo, filePath string) (RelatedWorktreeMatch, bool)

func NewSharedFileWatcher(root RootInfo, sessions SessionFileRecorder, worktreeResolvers ...WorktreeResolver) (*SharedFileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	var resolver WorktreeResolver
	if len(worktreeResolvers) > 0 {
		resolver = worktreeResolvers[0]
	}
	sw := &SharedFileWatcher{
		root:              root,
		watcher:           w,
		sessionStore:      sessions,
		worktreeResolver:  resolver,
		sessions:          make(map[string]*sessionInfo),
		watchedDirs:       make(map[string]struct{}),
		pendingWrites:     make(map[string]string),
		pendingChanges:    make(map[string]FileChangeEvent),
		pendingChangeDirs: make(map[string]struct{}),
		done:              make(chan struct{}),
		watchQueue:        make(chan string, maxWatchDirs),
	}
	if err := sw.WatchDir("."); err != nil {
		if closeErr := w.Close(); closeErr != nil {
		}
		return nil, err
	}
	go sw.run()
	go sw.runWatchQueue()
	return sw, nil
}

// runWatchQueue performs directory watches off the event-consumer goroutine.
// WatchDir reaches fsnotify.Add, which only completes once the fsnotify reader
// has written into watcher.Events; run() is that channel's sole consumer, so
// watching from run() would wait on itself forever.
func (sw *SharedFileWatcher) runWatchQueue() {
	for {
		select {
		case dir := <-sw.watchQueue:
			sw.watchNearestExistingDir(dir)
		case <-sw.done:
			return
		}
	}
}

// queueWatchDir schedules a directory watch without blocking the caller.
// Bounded by the queue capacity; dropping a request only costs an unwatched
// subdirectory, never a stuck scheduler.
func (sw *SharedFileWatcher) queueWatchDir(dirRel string) {
	select {
	case sw.watchQueue <- dirRel:
	case <-sw.done:
	default:
	}
}

func (sw *SharedFileWatcher) RegisterSession(sessionKey string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.sessions[sessionKey] = &sessionInfo{key: sessionKey}
}

func (sw *SharedFileWatcher) UnregisterSession(sessionKey string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	delete(sw.sessions, sessionKey)
	for path, key := range sw.pendingWrites {
		if key == sessionKey {
			delete(sw.pendingWrites, path)
		}
	}
}

func (sw *SharedFileWatcher) MarkSessionActive(_ string) {
}

func (sw *SharedFileWatcher) RecordPendingWrite(sessionKey, filePath string) {
	watchDir := ""
	sw.mu.Lock()
	if rel, err := sw.root.NormalizePath(filePath); err == nil {
		filePath = rel
	}
	filePath = filepath.ToSlash(filePath)
	sw.pendingWrites[filePath] = sessionKey
	watchDir = parentDir(filePath)
	sw.mu.Unlock()
	if watchDir != "" {
		// Same reason as run(): never reach fsnotify.Add on a caller that may
		// hold up session progress. The queue goroutine walks up to an
		// existing ancestor without blocking anyone.
		sw.queueWatchDir(watchDir)
	}
}

func (sw *SharedFileWatcher) RecordSessionFile(sessionKey, filePath string) {
	if sw.sessionStore == nil || sessionKey == "" || filePath == "" {
		return
	}
	sw.recordSessionWorktree(context.Background(), sessionKey, filePath)
	record, ok := sw.resolveRelatedFileRecord(context.Background(), filePath)
	if !ok {
		return
	}
	if err := sw.sessionStore.RecordOutputFileInRepo(
		context.Background(),
		sessionKey,
		record.rootID,
		record.repoKind,
		record.repoPath,
		record.repoName,
		record.path,
		record.head,
	); err != nil {
		return
	}
	if record.repoKind != "git" || sameCleanPath(record.repoPath, sw.root.RootPath) {
		sw.root.UpdateFileMeta(record.path, sessionKey, "agent")
	}
	sw.emitRelatedFile(RelatedFileEvent{
		RootID:     sw.root.ID,
		SessionKey: sessionKey,
		Path:       record.path,
	})
}

func (sw *SharedFileWatcher) resolveRelatedFileRecord(ctx context.Context, filePath string) (relatedFileRecord, bool) {
	absFilePath := absoluteRelatedFilePath(sw.root.RootPath, filePath)
	if sw.worktreeResolver != nil && filePath != "" {
		if match, ok := sw.worktreeResolver(ctx, sw.root, filePath); ok {
			relPath, ok := relativePathInside(match.Path, absFilePath)
			if ok && validRelatedPath(relPath) {
				repoPath := filepath.Clean(match.Path)
				return relatedFileRecord{
					rootID:   sw.root.ID,
					repoKind: "git",
					repoPath: repoPath,
					repoName: filepath.Base(repoPath),
					path:     relPath,
					head:     strings.TrimSpace(match.Head),
				}, true
			}
		}
	}

	relPath, err := sw.root.NormalizePath(filePath)
	if err != nil {
		return relatedFileRecord{}, false
	}
	relPath = filepath.ToSlash(relPath)
	if !validRelatedPath(relPath) {
		return relatedFileRecord{}, false
	}
	rootPath := filepath.Clean(sw.root.RootPath)
	return relatedFileRecord{
		rootID:   sw.root.ID,
		repoKind: "plain",
		repoPath: rootPath,
		repoName: sw.root.Name,
		path:     relPath,
	}, true
}

func absoluteRelatedFilePath(rootPath, filePath string) string {
	cleanPath := strings.TrimSpace(filePath)
	if cleanPath == "" {
		return ""
	}
	if filepath.IsAbs(cleanPath) {
		return filepath.Clean(cleanPath)
	}
	return filepath.Clean(filepath.Join(rootPath, cleanPath))
}

func validRelatedPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "." || path == ".." || path == "" {
		return false
	}
	return !(len(path) >= len(".mindfs") && path[:len(".mindfs")] == ".mindfs")
}

func relativePathInside(rootPath, filePath string) (string, bool) {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if rootPath == "" {
		return "", false
	}
	cleanPath := strings.TrimSpace(filePath)
	if cleanPath == "" {
		return "", false
	}
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(rootPath, cleanPath)
	}
	cleanPath = filepath.Clean(cleanPath)
	rel, err := filepath.Rel(rootPath, cleanPath)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func sameCleanPath(a, b string) bool {
	return filepath.Clean(strings.TrimSpace(a)) == filepath.Clean(strings.TrimSpace(b))
}

func (sw *SharedFileWatcher) recordSessionWorktree(ctx context.Context, sessionKey, filePath string) {
	if sw.sessionStore == nil || sw.worktreeResolver == nil || sessionKey == "" || filePath == "" {
		return
	}
	match, ok := sw.worktreeResolver(ctx, sw.root, filePath)
	if !ok {
		return
	}
	if added, err := sw.sessionStore.RecordRelatedWorktree(ctx, sessionKey, sw.root.ID, match.Path, match.Branch, match.Head); err == nil && added {
		sw.emitRelatedFile(RelatedFileEvent{
			RootID:          sw.root.ID,
			SessionKey:      sessionKey,
			RelatedWorktree: &match,
		})
	}
}

func (sw *SharedFileWatcher) SetOnFileChange(handler func(FileChangeEvent)) {
	sw.mu.Lock()
	sw.onFileChange = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SetOnFileChangeBatch(handler func(FileChangeBatchEvent)) {
	sw.mu.Lock()
	sw.onFileChangeBatch = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SetOnRelatedFile(handler func(RelatedFileEvent)) {
	sw.mu.Lock()
	sw.onRelatedFile = handler
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) SessionCount() int {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return len(sw.sessions)
}

func (sw *SharedFileWatcher) Close() {
	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return
	default:
		close(sw.done)
	}
	sw.mu.Unlock()
	sw.flushFileChangeBatch(0)
	sw.watcher.Close()
}

func (sw *SharedFileWatcher) run() {
	for {
		select {
		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			if sw.shouldIgnore(event.Name) {
				continue
			}
			rel, err := sw.root.NormalizePath(event.Name)
			if err != nil {
				continue
			}
			if event.Op&fsnotify.Remove != 0 {
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  false,
				})
				continue
			}
			info, err := os.Stat(event.Name)
			if err != nil {
				// File might disappear quickly during rename/remove races.
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  false,
				})
				continue
			}
			if info.IsDir() {
				sw.emitFileChange(FileChangeEvent{
					RootID: sw.root.ID,
					Path:   rel,
					Op:     event.Op.String(),
					IsDir:  true,
				})
				// Never call WatchDir synchronously here: it reaches
				// fsnotify.Add, which needs this very loop to drain
				// sw.watcher.Events before it can reply — a self-deadlock.
				sw.queueWatchDir(rel)
				continue
			}
			sw.emitFileChange(FileChangeEvent{
				RootID: sw.root.ID,
				Path:   rel,
				Op:     event.Op.String(),
				IsDir:  false,
			})
			sessionKey := sw.resolveSessionKey(rel)
			if sessionKey == "" {
				continue
			}
			sw.RecordSessionFile(sessionKey, rel)
		case _, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
		case <-sw.done:
			return
		}
	}
}

func (sw *SharedFileWatcher) resolveSessionKey(relPath string) string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sessionKey, ok := sw.pendingWrites[relPath]; ok {
		delete(sw.pendingWrites, relPath)
		return sessionKey
	}
	return ""
}

func (sw *SharedFileWatcher) emitFileChange(change FileChangeEvent) {
	sw.queueFileChangeBatch(change)
}

func (sw *SharedFileWatcher) queueFileChangeBatch(change FileChangeEvent) {
	change.Path = filepath.ToSlash(change.Path)
	if change.Path == "" {
		return
	}
	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return
	default:
	}
	if sw.pendingChanges == nil {
		sw.pendingChanges = make(map[string]FileChangeEvent)
	}
	if sw.pendingChangeDirs == nil {
		sw.pendingChangeDirs = make(map[string]struct{})
	}
	sw.pendingChanges[change.Path] = change
	sw.pendingChangeDirs[parentDir(change.Path)] = struct{}{}
	if change.IsDir || strings.Contains(change.Op, "REMOVE") || strings.Contains(change.Op, "RENAME") {
		sw.pendingChangeDirs[change.Path] = struct{}{}
	}
	sw.fileChangeVersion++
	version := sw.fileChangeVersion
	if sw.fileChangeTimer != nil {
		sw.fileChangeTimer.Stop()
	}
	sw.fileChangeTimer = time.AfterFunc(fileChangeBatchDelay, func() {
		sw.flushFileChangeBatch(version)
	})
	sw.mu.Unlock()
}

func (sw *SharedFileWatcher) flushFileChangeBatch(version uint64) {
	sw.mu.Lock()
	if version != 0 && version != sw.fileChangeVersion {
		sw.mu.Unlock()
		return
	}
	if sw.fileChangeTimer != nil {
		sw.fileChangeTimer.Stop()
		sw.fileChangeTimer = nil
	}
	if len(sw.pendingChanges) == 0 {
		sw.mu.Unlock()
		return
	}
	changesByPath := sw.pendingChanges
	dirsByPath := sw.pendingChangeDirs
	sw.pendingChanges = make(map[string]FileChangeEvent)
	sw.pendingChangeDirs = make(map[string]struct{})
	batchHandler := sw.onFileChangeBatch
	singleHandler := sw.onFileChange
	sw.mu.Unlock()

	paths := make([]string, 0, len(changesByPath))
	for path := range changesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	dirs := make([]string, 0, len(dirsByPath))
	for dir := range dirsByPath {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	events := make([]FileChangeEvent, 0, len(paths))
	for _, path := range paths {
		events = append(events, changesByPath[path])
	}

	if batchHandler != nil {
		batchHandler(FileChangeBatchEvent{
			RootID: sw.root.ID,
			Paths:  paths,
			Dirs:   dirs,
			Events: events,
		})
		return
	}
	if singleHandler != nil {
		for _, change := range events {
			singleHandler(change)
		}
	}
}

func parentDir(path string) string {
	clean := strings.Trim(filepath.ToSlash(path), "/")
	if clean == "" || clean == "." {
		return "."
	}
	idx := strings.LastIndex(clean, "/")
	if idx <= 0 {
		return "."
	}
	return clean[:idx]
}

func (sw *SharedFileWatcher) emitRelatedFile(change RelatedFileEvent) {
	sw.mu.RLock()
	handler := sw.onRelatedFile
	sw.mu.RUnlock()
	if handler != nil {
		handler(change)
	}
}

func (sw *SharedFileWatcher) WatchDir(dirRel string) error {
	dirAbs, err := sw.root.resolveRelativePath(dirRel)
	if err != nil {
		return err
	}
	if sw.shouldIgnore(dirAbs) {
		return nil
	}
	info, err := os.Stat(dirAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	clean := filepath.Clean(dirAbs)
	// Claim the path under the lock, but call fsnotify.Add outside it: on Windows
	// AddWith blocks on an IOCP completion, and holding sw.mu across that call
	// deadlocks every other user of the watcher (RegisterSession, flush, ...).
	sw.mu.Lock()
	select {
	case <-sw.done:
		sw.mu.Unlock()
		return nil
	default:
	}
	if _, ok := sw.watchedDirs[clean]; ok {
		sw.mu.Unlock()
		return nil
	}
	if len(sw.watchedDirs) >= maxWatchDirs {
		sw.mu.Unlock()
		return nil
	}
	sw.watchedDirs[clean] = struct{}{} // provisional claim; released on failure
	sw.mu.Unlock()

	if err := sw.watcher.Add(clean); err != nil {
		sw.mu.Lock()
		delete(sw.watchedDirs, clean)
		sw.mu.Unlock()
		return err
	}
	return nil
}

func (sw *SharedFileWatcher) watchNearestExistingDir(dirRel string) {
	dirRel = strings.TrimSpace(filepath.ToSlash(dirRel))
	if dirRel == "" {
		dirRel = "."
	}
	for {
		if err := sw.WatchDir(dirRel); err == nil {
			return
		}
		next := parentDir(dirRel)
		if next == dirRel {
			return
		}
		dirRel = next
	}
}

func (sw *SharedFileWatcher) shouldIgnore(path string) bool {
	metaDir := sw.root.MetaDir()
	if metaDir != "" && isPathWithin(path, metaDir) {
		return true
	}
	checkPath := path
	if filepath.IsAbs(path) {
		if rel, err := sw.root.relativeFromAbsolute(path); err == nil {
			checkPath = rel
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(checkPath)), "/") {
		if part == "" {
			continue
		}
		if _, ignored := ignoredWatchNames[part]; ignored {
			return true
		}
	}
	return false
}

func isPathWithin(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
