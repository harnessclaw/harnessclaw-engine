package task

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Store defines the interface for task persistence.
type Store interface {
	Create(ctx context.Context, task *Task) (*Task, error)
	Get(ctx context.Context, scopeID, id string) (*Task, error)
	List(ctx context.Context, scopeID string) ([]*Task, error)
	Update(ctx context.Context, scopeID, id string, updates *TaskUpdate) (*Task, error)
	Delete(ctx context.Context, scopeID, id string) error
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu    sync.RWMutex
	tasks map[string]map[string]*Task // scopeID -> taskID -> Task
	seq   map[string]int              // scopeID -> next sequence number
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks: make(map[string]map[string]*Task),
		seq:   make(map[string]int),
	}
}

func (ms *MemoryStore) Create(_ context.Context, task *Task) (*Task, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if _, ok := ms.tasks[task.ScopeID]; !ok {
		ms.tasks[task.ScopeID] = make(map[string]*Task)
		ms.seq[task.ScopeID] = 0
	}
	ms.seq[task.ScopeID]++
	task.ID = strconv.Itoa(ms.seq[task.ScopeID])
	task.Status = TaskStatusPending
	task.CreatedAt = time.Now()
	task.UpdatedAt = task.CreatedAt

	// Store a copy
	cp := *task
	ms.tasks[task.ScopeID][task.ID] = &cp
	return task, nil
}

func (ms *MemoryStore) Get(_ context.Context, scopeID, id string) (*Task, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	scope, ok := ms.tasks[scopeID]
	if !ok {
		return nil, fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	t, ok := scope[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	cp := *t
	return &cp, nil
}

func (ms *MemoryStore) List(_ context.Context, scopeID string) ([]*Task, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	scope := ms.tasks[scopeID]
	result := make([]*Task, 0, len(scope))
	for _, t := range scope {
		if t.Status == "deleted" {
			continue
		}
		cp := *t
		result = append(result, &cp)
	}
	return result, nil
}

func (ms *MemoryStore) Update(_ context.Context, scopeID, id string, updates *TaskUpdate) (*Task, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	scope, ok := ms.tasks[scopeID]
	if !ok {
		return nil, fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	t, ok := scope[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}

	if updates.Subject != nil {
		t.Subject = *updates.Subject
	}
	if updates.Description != nil {
		t.Description = *updates.Description
	}
	if updates.Status != nil {
		t.Status = *updates.Status
	}
	if updates.Owner != nil {
		t.Owner = *updates.Owner
	}
	if updates.ActiveForm != nil {
		t.ActiveForm = *updates.ActiveForm
	}
	if len(updates.AddBlocks) > 0 {
		t.Blocks = append(t.Blocks, updates.AddBlocks...)
	}
	if len(updates.AddBlockedBy) > 0 {
		t.BlockedBy = append(t.BlockedBy, updates.AddBlockedBy...)
	}
	if updates.Metadata != nil {
		if t.Metadata == nil {
			t.Metadata = make(map[string]any)
		}
		for k, v := range updates.Metadata {
			if v == nil {
				delete(t.Metadata, k)
			} else {
				t.Metadata[k] = v
			}
		}
	}
	t.UpdatedAt = time.Now()

	cp := *t
	return &cp, nil
}

func (ms *MemoryStore) Delete(_ context.Context, scopeID, id string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	scope, ok := ms.tasks[scopeID]
	if !ok {
		return fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	if _, ok := scope[id]; !ok {
		return fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	delete(scope, id)
	return nil
}
