package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_tasks.db")

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create
	task1, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "task one", Description: "desc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task1.ID != "1" {
		t.Errorf("expected ID=1, got %s", task1.ID)
	}
	if task1.Status != TaskStatusPending {
		t.Errorf("expected status=pending, got %s", task1.Status)
	}

	task2, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "task two"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task2.ID != "2" {
		t.Errorf("expected ID=2, got %s", task2.ID)
	}

	// Get
	got, err := store.Get(ctx, "s1", "1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != "task one" {
		t.Errorf("expected subject 'task one', got %q", got.Subject)
	}

	// List
	list, err := store.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(list))
	}

	// Update
	inProgress := TaskStatus("in_progress")
	owner := "agent-1"
	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{Status: &inProgress, Owner: &owner})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != "in_progress" {
		t.Errorf("expected status in_progress, got %s", updated.Status)
	}
	if updated.Owner != "agent-1" {
		t.Errorf("expected owner agent-1, got %s", updated.Owner)
	}

	// Delete
	err = store.Delete(ctx, "s1", "2")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = store.List(ctx, "s1")
	if len(list) != 1 {
		t.Errorf("expected 1 task after delete, got %d", len(list))
	}

	// Verify DB file exists.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("expected sqlite db file to exist")
	}
}

func TestSQLiteStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")
	ctx := context.Background()

	// Write data.
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	_, err = store1.Create(ctx, &Task{ScopeID: "s1", Subject: "persistent task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store1.Close()

	// Reopen and verify.
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer store2.Close()

	list, err := store2.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 persisted task, got %d", len(list))
	}
	if list[0].Subject != "persistent task" {
		t.Errorf("expected subject 'persistent task', got %q", list[0].Subject)
	}

	// Sequence should continue from where it left off.
	task2, err := store2.Create(ctx, &Task{ScopeID: "s1", Subject: "second"})
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	if task2.ID != "2" {
		t.Errorf("expected sequence to continue at 2, got %s", task2.ID)
	}
}

func TestSQLiteStore_Scopes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "scopes.db")
	ctx := context.Background()

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// Different scopes have independent sequences and data.
	t1, _ := store.Create(ctx, &Task{ScopeID: "team-a", Subject: "a1"})
	t2, _ := store.Create(ctx, &Task{ScopeID: "team-b", Subject: "b1"})

	if t1.ID != "1" || t2.ID != "1" {
		t.Errorf("expected independent IDs (1,1), got (%s,%s)", t1.ID, t2.ID)
	}

	listA, _ := store.List(ctx, "team-a")
	listB, _ := store.List(ctx, "team-b")
	if len(listA) != 1 || len(listB) != 1 {
		t.Errorf("expected 1 task per scope, got %d, %d", len(listA), len(listB))
	}
}
