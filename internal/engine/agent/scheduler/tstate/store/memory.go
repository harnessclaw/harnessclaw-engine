package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

// Memory is a thread-safe in-process Store for tstate.
type Memory struct {
	mu   sync.Mutex
	rows map[types.TaskID]*tstate.TaskState
}

func NewMemory() *Memory {
	return &Memory{rows: map[types.TaskID]*tstate.TaskState{}}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) Insert(_ context.Context, ts tstate.TaskState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[ts.ID]; ok {
		return fmt.Errorf("store: duplicate id %s", ts.ID)
	}
	cp := ts
	m.rows[ts.ID] = &cp
	return nil
}

func (m *Memory) Delete(_ context.Context, id types.TaskID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

func (m *Memory) Get(_ context.Context, id types.TaskID) (tstate.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return tstate.TaskState{}, fmt.Errorf("store: not found %s", id)
	}
	return *r, nil
}

func (m *Memory) CAS(_ context.Context, id types.TaskID, expect, set types.Status, mut Mutation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("store: not found %s", id)
	}
	if r.Status != expect {
		return fmt.Errorf("store: cas miss: id=%s want=%s got=%s", id, expect, r.Status)
	}
	r.Status = set
	applyMutation(r, mut)
	return nil
}

func (m *Memory) UpdateField(_ context.Context, id types.TaskID, field string, value any, attemptGuard int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("store: not found %s", id)
	}
	if r.Attempt != attemptGuard {
		return fmt.Errorf("store: epoch mismatch: id=%s attempt=%d guard=%d", id, r.Attempt, attemptGuard)
	}
	switch field {
	case tstate.FieldStagedResultRef:
		v, ok := value.(types.Ref)
		if !ok {
			return errors.New("store: staged_result_ref must be types.Ref")
		}
		r.StagedResultRef = v
	default:
		return fmt.Errorf("store: UpdateField rejected field=%q", field)
	}
	return nil
}

func (m *Memory) ListByStatus(_ context.Context, team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tstate.TaskState
	for _, r := range m.rows {
		if r.Status != st {
			continue
		}
		if team != "" && r.TeamID != team {
			continue
		}
		out = append(out, *r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *Memory) ListByParent(_ context.Context, parent types.TaskID) ([]tstate.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tstate.TaskState
	for _, r := range m.rows {
		if r.ParentID == parent {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *Memory) ListPendingDependentOn(_ context.Context, depID types.TaskID) ([]tstate.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tstate.TaskState
	for _, r := range m.rows {
		if r.Status != types.StatusPending {
			continue
		}
		for _, d := range r.Deps {
			if d == depID {
				out = append(out, *r)
				break
			}
		}
	}
	return out, nil
}

func (m *Memory) InTx(_ context.Context, fn func(Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(&memTx{m: m})
}

type memTx struct{ m *Memory }

func (t *memTx) Get(id types.TaskID) (tstate.TaskState, error) {
	r, ok := t.m.rows[id]
	if !ok {
		return tstate.TaskState{}, fmt.Errorf("store: not found %s", id)
	}
	return *r, nil
}

func (t *memTx) ListChildren(parent types.TaskID) ([]tstate.TaskState, error) {
	var out []tstate.TaskState
	for _, r := range t.m.rows {
		if r.ParentID == parent {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (t *memTx) ListByStatus(team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error) {
	var out []tstate.TaskState
	for _, r := range t.m.rows {
		if r.Status != st {
			continue
		}
		if team != "" && r.TeamID != team {
			continue
		}
		out = append(out, *r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (t *memTx) CAS(id types.TaskID, expect, set types.Status, mut Mutation) error {
	r, ok := t.m.rows[id]
	if !ok {
		return fmt.Errorf("store: not found %s", id)
	}
	if r.Status != expect {
		return fmt.Errorf("store: cas miss: id=%s want=%s got=%s", id, expect, r.Status)
	}
	r.Status = set
	applyMutation(r, mut)
	return nil
}

func (t *memTx) Insert(ts tstate.TaskState) error {
	if _, ok := t.m.rows[ts.ID]; ok {
		return fmt.Errorf("store: duplicate id %s", ts.ID)
	}
	cp := ts
	t.m.rows[ts.ID] = &cp
	return nil
}

func (t *memTx) Delete(id types.TaskID) error {
	delete(t.m.rows, id)
	return nil
}

func applyMutation(r *tstate.TaskState, mut Mutation) {
	if mut.Lease != nil {
		r.Lease = *mut.Lease
	}
	if mut.Attempt != nil {
		r.Attempt = *mut.Attempt
	}
	if mut.ResultRef != nil {
		r.ResultRef = *mut.ResultRef
	}
	if mut.StagedResultRef != nil {
		r.StagedResultRef = *mut.StagedResultRef
	}
	if mut.WaitingFor != nil {
		r.WaitingFor = append(r.WaitingFor[:0], mut.WaitingFor...)
	}
	if mut.LastError != nil {
		r.LastError = *mut.LastError
	}
	if mut.FailedReason != nil {
		r.FailedReason = *mut.FailedReason
	}
}
