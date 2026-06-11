package task

import (
	"context"
	"testing"
)

func TestMemoryStore_CreateAssignsIncrementingIDs(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	t1, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "first"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if t1.ID != "1" {
		t.Errorf("expected ID '1', got %q", t1.ID)
	}

	t2, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "second"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if t2.ID != "2" {
		t.Errorf("expected ID '2', got %q", t2.ID)
	}

	// Different scope resets sequence
	t3, err := store.Create(ctx, &Task{ScopeID: "s2", Subject: "other scope"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if t3.ID != "1" {
		t.Errorf("expected ID '1' for new scope, got %q", t3.ID)
	}
}

func TestMemoryStore_CreateSetsStatusAndTimestamps(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	task, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskStatusPending {
		t.Errorf("expected status 'pending', got %q", task.Status)
	}
	if task.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if task.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
	if !task.CreatedAt.Equal(task.UpdatedAt) {
		t.Error("expected CreatedAt and UpdatedAt to be equal on creation")
	}
}

func TestMemoryStore_GetReturnsCopy(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, err := store.Create(ctx, &Task{ScopeID: "s1", Subject: "original"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got1, err := store.Get(ctx, "s1", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got2, err := store.Get(ctx, "s1", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutating got1 should not affect got2
	got1.Subject = "mutated"
	if got2.Subject != "original" {
		t.Errorf("expected got2.Subject to remain 'original', got %q", got2.Subject)
	}
}

func TestMemoryStore_GetNotFound(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Non-existent scope
	_, err := store.Get(ctx, "missing", "1")
	if err == nil {
		t.Error("expected error for non-existent scope")
	}

	// Create a task in scope, then look up wrong ID
	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "exists"})
	_, err = store.Get(ctx, "s1", "999")
	if err == nil {
		t.Error("expected error for non-existent task ID")
	}
}

func TestMemoryStore_ListExcludesDeletedTasks(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "keep"})
	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "remove"})

	// Delete the second task
	err := store.Delete(ctx, "s1", "2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tasks, err := store.List(ctx, "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Subject != "keep" {
		t.Errorf("expected subject 'keep', got %q", tasks[0].Subject)
	}
}

func TestMemoryStore_ListEmptyScope(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	tasks, err := store.List(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestMemoryStore_UpdateModifiesOnlySpecifiedFields(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{
		ScopeID:     "s1",
		Subject:     "original subject",
		Description: "original description",
	})

	newSubject := "updated subject"
	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		Subject: &newSubject,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Subject != "updated subject" {
		t.Errorf("expected subject 'updated subject', got %q", updated.Subject)
	}
	if updated.Description != "original description" {
		t.Errorf("expected description to remain 'original description', got %q", updated.Description)
	}
	if updated.UpdatedAt.Equal(updated.CreatedAt) {
		t.Error("expected UpdatedAt to be changed after update")
	}
}

func TestMemoryStore_UpdateStatusAndOwner(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "task"})

	status := TaskStatusInProgress
	owner := "agent-1"
	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		Status: &status,
		Owner:  &owner,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Status != TaskStatusInProgress {
		t.Errorf("expected status 'in_progress', got %q", updated.Status)
	}
	if updated.Owner != "agent-1" {
		t.Errorf("expected owner 'agent-1', got %q", updated.Owner)
	}
}

func TestMemoryStore_UpdateAddBlocksAppends(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "blocker"})

	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		AddBlocks: []string{"2", "3"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(updated.Blocks))
	}

	// Append more
	updated, err = store.Update(ctx, "s1", "1", &TaskUpdate{
		AddBlocks: []string{"4"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.Blocks) != 3 {
		t.Fatalf("expected 3 blocks after append, got %d", len(updated.Blocks))
	}
}

func TestMemoryStore_UpdateAddBlockedByAppends(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "blocked"})

	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		AddBlockedBy: []string{"5"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.BlockedBy) != 1 {
		t.Fatalf("expected 1 blockedBy, got %d", len(updated.BlockedBy))
	}

	updated, err = store.Update(ctx, "s1", "1", &TaskUpdate{
		AddBlockedBy: []string{"6", "7"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.BlockedBy) != 3 {
		t.Fatalf("expected 3 blockedBy after append, got %d", len(updated.BlockedBy))
	}
}

func TestMemoryStore_UpdateMetadataNilValueDeletesKey(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "meta"})

	// Set some metadata
	_, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		Metadata: map[string]any{"key1": "value1", "key2": "value2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Delete key1 by setting nil, update key2
	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		Metadata: map[string]any{"key1": nil, "key2": "updated"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := updated.Metadata["key1"]; ok {
		t.Error("expected key1 to be deleted")
	}
	if updated.Metadata["key2"] != "updated" {
		t.Errorf("expected key2='updated', got %v", updated.Metadata["key2"])
	}
}

func TestMemoryStore_UpdateNotFound(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Non-existent scope
	_, err := store.Update(ctx, "missing", "1", &TaskUpdate{})
	if err == nil {
		t.Error("expected error for non-existent scope")
	}

	// Existing scope, wrong ID
	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "exists"})
	_, err = store.Update(ctx, "s1", "999", &TaskUpdate{})
	if err == nil {
		t.Error("expected error for non-existent task ID")
	}
}

func TestMemoryStore_DeleteRemovesTask(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "doomed"})

	err := store.Delete(ctx, "s1", "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it's gone
	_, err = store.Get(ctx, "s1", "1")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestMemoryStore_DeleteNotFound(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Non-existent scope
	err := store.Delete(ctx, "missing", "1")
	if err == nil {
		t.Error("expected error for non-existent scope")
	}

	// Existing scope, wrong ID
	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "exists"})
	err = store.Delete(ctx, "s1", "999")
	if err == nil {
		t.Error("expected error for non-existent task ID")
	}
}

func TestMemoryStore_UpdateActiveForm(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	_, _ = store.Create(ctx, &Task{ScopeID: "s1", Subject: "spinner"})

	form := "processing data"
	updated, err := store.Update(ctx, "s1", "1", &TaskUpdate{
		ActiveForm: &form,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.ActiveForm != "processing data" {
		t.Errorf("expected activeForm 'processing data', got %q", updated.ActiveForm)
	}
}
