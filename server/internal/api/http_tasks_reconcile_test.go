package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mindfs/server/internal/fs"
	"mindfs/server/internal/kanban"
)

type reconcileRootProvider struct {
	root fs.RootInfo
}

func (p reconcileRootProvider) GetRoot(rootID string) (fs.RootInfo, error) {
	if rootID != p.root.ID {
		return fs.RootInfo{}, errors.New("root not found")
	}
	return p.root, nil
}

func (p reconcileRootProvider) ListRoots() []fs.RootInfo { return []fs.RootInfo{p.root} }

// The board refreshes through the incremental `after` path, which only reports
// tasks that still exist -- a task deleted while the page was disconnected
// stays on the board forever. `ids=1` is the escape hatch the client uses to
// reconcile, so it must report the current set without pagination.
func TestTaskListIDsReflectsDeletions(t *testing.T) {
	root := fs.RootInfo{ID: "root", RootPath: t.TempDir(), MetaLocation: fs.MetaLocationProject}

	ctx := context.Background()
	store, err := kanban.NewTaskStore(root)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"task-a", "task-b"} {
		created := base.Add(time.Duration(i) * time.Minute)
		if _, err := store.CreateTask(ctx,
			kanban.Task{ID: id, TaskNumber: i + 1, RootID: "root", CreatedAt: created, UpdatedAt: created},
			kanban.StageRun{ID: "run-" + id, TaskID: id, CreatedAt: created, UpdatedAt: created},
			kanban.TaskEvent{ID: "event-" + id, TaskID: id, CreatedAt: created}); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// The service opens its own handle; the ids below must come from it, not
	// from the store we just wrote through.
	service := kanban.NewService(nil, reconcileRootProvider{root: root})
	defer service.Close()

	handler := &HTTPHandler{AppContext: &AppContext{Kanban: service}}
	readIDs := func() []string {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.handleKanbanTasksList(rec, httptest.NewRequest(http.MethodGet, "/api/tasks?root=root&ids=1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.IDs
	}

	if got := readIDs(); len(got) != 2 {
		t.Fatalf("got %d ids, want 2 (%v)", len(got), got)
	}

	if err := service.DeleteTask(ctx, "root", "task-a"); err != nil {
		t.Fatal(err)
	}
	ids := readIDs()
	if len(ids) != 1 || ids[0] != "task-b" {
		t.Fatalf("after delete got %v, want [task-b]", ids)
	}
}
