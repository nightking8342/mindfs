package kanban

import (
	"context"
	"testing"
	"time"

	"mindfs/server/internal/fs"
)

// The incremental `after` filter only reports tasks that still exist, so a
// client that was offline during a deletion never learns about it. ListTaskIDs
// is what lets the client reconcile; it must stay complete and unfiltered.
func TestListTaskIDsReportsDeletionsForReconciliation(t *testing.T) {
	ctx := context.Background()
	store, err := NewTaskStore(fs.RootInfo{ID: "root", RootPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"task-a", "task-b", "task-c"} {
		created := base.Add(time.Duration(i) * time.Minute)
		if _, err := store.CreateTask(ctx,
			Task{ID: id, TaskNumber: i + 1, RootID: "root", CreatedAt: created, UpdatedAt: created},
			StageRun{ID: "run-" + id, TaskID: id, CreatedAt: created, UpdatedAt: created},
			TaskEvent{ID: "event-" + id, TaskID: id, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := store.ListTaskIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3 (%v)", len(ids), ids)
	}

	// Remove task-b the way a direct store call would, then confirm the id list
	// no longer reports it: that absence is the only signal a client can use.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, "task-b"); err != nil {
		t.Fatal(err)
	}
	ids, err = store.ListTaskIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids after delete, want 2 (%v)", len(ids), ids)
	}
	for _, id := range ids {
		if id == "task-b" {
			t.Fatalf("deleted task still reported: %v", ids)
		}
	}

	// The incremental path simply omits the deleted task instead of reporting
	// it. A client merging incrementally therefore keeps its stale copy
	// forever -- hence the full id list.
	incremental, err := store.ListTasks(ctx, ListTasksOptions{After: base.Add(-time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if len(incremental) != 2 {
		t.Fatalf("incremental returned %d tasks, want 2", len(incremental))
	}
	for _, task := range incremental {
		if task.ID == "task-b" {
			t.Fatalf("incremental reported the deleted task: %v", incremental)
		}
	}
}
