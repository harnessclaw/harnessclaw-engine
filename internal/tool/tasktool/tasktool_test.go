package tasktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/task"
)

func newTestStore() task.Store {
	return task.NewMemoryStore()
}

// ---------------------------------------------------------------------------
// CreateTool tests
// ---------------------------------------------------------------------------

func TestCreateTool_ValidInput(t *testing.T) {
	store := newTestStore()
	ct := NewCreate(store, "scope1")

	input := json.RawMessage(`{"subject":"Build API","description":"Implement REST endpoints"}`)
	result, err := ct.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Verify the returned task has an ID and correct fields
	var created task.Task
	if err := json.Unmarshal([]byte(result.Content), &created); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if created.ID == "" {
		t.Error("expected ID to be assigned")
	}
	if created.Subject != "Build API" {
		t.Errorf("expected subject 'Build API', got %q", created.Subject)
	}
	if created.Status != task.TaskStatusPending {
		t.Errorf("expected status 'pending', got %q", created.Status)
	}
}

func TestCreateTool_WithActiveForm(t *testing.T) {
	store := newTestStore()
	ct := NewCreate(store, "scope1")

	input := json.RawMessage(`{"subject":"Deploy","description":"Deploy to prod","activeForm":"deploying"}`)
	result, err := ct.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var created task.Task
	if err := json.Unmarshal([]byte(result.Content), &created); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if created.ActiveForm != "deploying" {
		t.Errorf("expected activeForm 'deploying', got %q", created.ActiveForm)
	}
}

func TestCreateTool_MissingSubjectErrors(t *testing.T) {
	store := newTestStore()
	ct := NewCreate(store, "scope1")

	err := ct.ValidateInput(json.RawMessage(`{"description":"no subject"}`))
	if err == nil {
		t.Error("expected validation error for missing subject")
	}
}

func TestCreateTool_MissingDescriptionErrors(t *testing.T) {
	store := newTestStore()
	ct := NewCreate(store, "scope1")

	err := ct.ValidateInput(json.RawMessage(`{"subject":"no desc"}`))
	if err == nil {
		t.Error("expected validation error for missing description")
	}
}

func TestCreateTool_InvalidJSONErrors(t *testing.T) {
	store := newTestStore()
	ct := NewCreate(store, "scope1")

	err := ct.ValidateInput(json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected validation error for invalid JSON")
	}
}

func TestCreateTool_Metadata(t *testing.T) {
	ct := NewCreate(newTestStore(), "s1")
	if ct.Name() != "TaskCreate" {
		t.Errorf("expected name 'TaskCreate', got %q", ct.Name())
	}
	if ct.IsReadOnly() {
		t.Error("expected IsReadOnly to be false")
	}
	if !ct.IsConcurrencySafe() {
		t.Error("expected IsConcurrencySafe to be true")
	}
}

// ---------------------------------------------------------------------------
// GetTool tests
// ---------------------------------------------------------------------------

func TestGetTool_ValidID(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	// Seed a task
	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "test task", Description: "desc"})

	gt := NewGet(store, "scope1")
	input := json.RawMessage(`{"taskId":"1"}`)
	result, err := gt.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var got task.Task
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if got.Subject != "test task" {
		t.Errorf("expected subject 'test task', got %q", got.Subject)
	}
}

func TestGetTool_InvalidID(t *testing.T) {
	store := newTestStore()
	gt := NewGet(store, "scope1")

	input := json.RawMessage(`{"taskId":"999"}`)
	result, err := gt.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent task")
	}
}

func TestGetTool_ValidateInput_MissingTaskID(t *testing.T) {
	gt := NewGet(newTestStore(), "s1")
	err := gt.ValidateInput(json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected validation error for missing taskId")
	}
}

func TestGetTool_ValidateInput_InvalidJSON(t *testing.T) {
	gt := NewGet(newTestStore(), "s1")
	err := gt.ValidateInput(json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected validation error for invalid JSON")
	}
}

func TestGetTool_Metadata(t *testing.T) {
	gt := NewGet(newTestStore(), "s1")
	if gt.Name() != "TaskGet" {
		t.Errorf("expected name 'TaskGet', got %q", gt.Name())
	}
	if !gt.IsReadOnly() {
		t.Error("expected IsReadOnly to be true")
	}
	if !gt.IsConcurrencySafe() {
		t.Error("expected IsConcurrencySafe to be true")
	}
}

// ---------------------------------------------------------------------------
// ListTool tests
// ---------------------------------------------------------------------------

func TestListTool_ReturnsAllNonDeletedTasks(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "task A", Description: "a"})
	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "task B", Description: "b"})
	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "task C", Description: "c"})

	// Delete task B
	store.Delete(ctx, "scope1", "2")

	lt := NewList(store, "scope1")
	result, err := lt.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var summaries []struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Content), &summaries); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(summaries))
	}
}

func TestListTool_SummaryFormat(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "my task", Description: "desc"})

	status := task.TaskStatusInProgress
	owner := "agent-1"
	store.Update(ctx, "scope1", "1", &task.TaskUpdate{Status: &status, Owner: &owner})

	lt := NewList(store, "scope1")
	result, err := lt.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var summaries []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Owner  string `json:"owner"`
	}
	if err := json.Unmarshal([]byte(result.Content), &summaries); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Status != "in_progress" {
		t.Errorf("expected status 'in_progress', got %q", summaries[0].Status)
	}
	if summaries[0].Owner != "agent-1" {
		t.Errorf("expected owner 'agent-1', got %q", summaries[0].Owner)
	}
}

func TestListTool_FiltersBlockedByCompletedTasks(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	// Create blocker (task 1) and blocked (task 2)
	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "blocker", Description: "b"})
	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "blocked", Description: "b"})

	// Mark task 2 as blocked by task 1
	store.Update(ctx, "scope1", "2", &task.TaskUpdate{AddBlockedBy: []string{"1"}})

	// Before completing blocker, list should show blockedBy
	lt := NewList(store, "scope1")
	result, _ := lt.Execute(ctx, nil)
	if !strings.Contains(result.Content, `"blockedBy"`) {
		t.Error("expected blockedBy in output when blocker is pending")
	}

	// Complete the blocker
	completed := task.TaskStatusCompleted
	store.Update(ctx, "scope1", "1", &task.TaskUpdate{Status: &completed})

	// Now blockedBy should be filtered out
	result, _ = lt.Execute(ctx, nil)
	var summaries []struct {
		ID        string   `json:"id"`
		BlockedBy []string `json:"blockedBy"`
	}
	json.Unmarshal([]byte(result.Content), &summaries)
	for _, s := range summaries {
		if s.ID == "2" && len(s.BlockedBy) > 0 {
			t.Error("expected blockedBy to be empty after blocker completed")
		}
	}
}

func TestListTool_EmptyScope(t *testing.T) {
	store := newTestStore()
	lt := NewList(store, "empty")
	result, err := lt.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "[]" {
		t.Errorf("expected empty array '[]', got %q", result.Content)
	}
}

func TestListTool_Metadata(t *testing.T) {
	lt := NewList(newTestStore(), "s1")
	if lt.Name() != "TaskList" {
		t.Errorf("expected name 'TaskList', got %q", lt.Name())
	}
	if !lt.IsReadOnly() {
		t.Error("expected IsReadOnly to be true")
	}
	if !lt.IsConcurrencySafe() {
		t.Error("expected IsConcurrencySafe to be true")
	}
}

// ---------------------------------------------------------------------------
// UpdateTool tests
// ---------------------------------------------------------------------------

func TestUpdateTool_StatusChange(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "update me", Description: "d"})

	ut := NewUpdate(store, "scope1")
	input := json.RawMessage(`{"taskId":"1","status":"in_progress"}`)
	result, err := ut.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var updated task.Task
	json.Unmarshal([]byte(result.Content), &updated)
	if updated.Status != task.TaskStatusInProgress {
		t.Errorf("expected status 'in_progress', got %q", updated.Status)
	}
}

func TestUpdateTool_OwnerChange(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "assign me", Description: "d"})

	ut := NewUpdate(store, "scope1")
	input := json.RawMessage(`{"taskId":"1","owner":"agent-42"}`)
	result, err := ut.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var updated task.Task
	json.Unmarshal([]byte(result.Content), &updated)
	if updated.Owner != "agent-42" {
		t.Errorf("expected owner 'agent-42', got %q", updated.Owner)
	}
}

func TestUpdateTool_DeleteViaStatus(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "delete me", Description: "d"})

	ut := NewUpdate(store, "scope1")
	input := json.RawMessage(`{"taskId":"1","status":"deleted"}`)
	result, err := ut.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "deleted") {
		t.Errorf("expected 'deleted' in content, got %q", result.Content)
	}

	// Verify actually deleted
	_, getErr := store.Get(ctx, "scope1", "1")
	if getErr == nil {
		t.Error("expected error when getting deleted task")
	}
}

func TestUpdateTool_NotFound(t *testing.T) {
	store := newTestStore()
	ut := NewUpdate(store, "scope1")

	input := json.RawMessage(`{"taskId":"999","status":"completed"}`)
	result, err := ut.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for non-existent task")
	}
}

func TestUpdateTool_ValidateInput_MissingTaskID(t *testing.T) {
	ut := NewUpdate(newTestStore(), "s1")
	err := ut.ValidateInput(json.RawMessage(`{"status":"completed"}`))
	if err == nil {
		t.Error("expected validation error for missing taskId")
	}
}

func TestUpdateTool_ValidateInput_InvalidJSON(t *testing.T) {
	ut := NewUpdate(newTestStore(), "s1")
	err := ut.ValidateInput(json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected validation error for invalid JSON")
	}
}

func TestUpdateTool_Metadata(t *testing.T) {
	ut := NewUpdate(newTestStore(), "s1")
	if ut.Name() != "TaskUpdate" {
		t.Errorf("expected name 'TaskUpdate', got %q", ut.Name())
	}
	if ut.IsReadOnly() {
		t.Error("expected IsReadOnly to be false")
	}
	if !ut.IsConcurrencySafe() {
		t.Error("expected IsConcurrencySafe to be true")
	}
}

func TestUpdateTool_MultipleFieldsAtOnce(t *testing.T) {
	store := newTestStore()
	ctx := context.Background()

	store.Create(ctx, &task.Task{ScopeID: "scope1", Subject: "original", Description: "orig desc"})

	ut := NewUpdate(store, "scope1")
	input := json.RawMessage(`{"taskId":"1","subject":"new subject","status":"in_progress","owner":"bot-1","activeForm":"working"}`)
	result, err := ut.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	var updated task.Task
	json.Unmarshal([]byte(result.Content), &updated)
	if updated.Subject != "new subject" {
		t.Errorf("expected subject 'new subject', got %q", updated.Subject)
	}
	if updated.Status != task.TaskStatusInProgress {
		t.Errorf("expected status 'in_progress', got %q", updated.Status)
	}
	if updated.Owner != "bot-1" {
		t.Errorf("expected owner 'bot-1', got %q", updated.Owner)
	}
	if updated.ActiveForm != "working" {
		t.Errorf("expected activeForm 'working', got %q", updated.ActiveForm)
	}
	// Description should remain unchanged
	if updated.Description != "orig desc" {
		t.Errorf("expected description 'orig desc', got %q", updated.Description)
	}
}

func TestUpdateTool_DeleteNotFoundErrors(t *testing.T) {
	store := newTestStore()
	ut := NewUpdate(store, "scope1")

	input := json.RawMessage(`{"taskId":"999","status":"deleted"}`)
	result, err := ut.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for deleting non-existent task")
	}
}
