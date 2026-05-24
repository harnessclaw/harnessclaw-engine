package tstate

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// Field name constants for Store.UpdateField. Keep this set as the only allowed
// values — callers pass them by name and the Store implementation switches on them.
const (
	FieldStagedResultRef = "staged_result_ref"
)

// Mutation is a struct of optional fields to update on a row.
// Nil pointer = field unchanged.
type Mutation struct {
	Status          *types.Status
	Lease           *types.Lease
	Attempt         *int
	ResultRef       *types.Ref
	StagedResultRef *types.Ref
	WaitingFor      []types.TaskID // nil = unchanged; empty slice = clear
	LastError       *string
	FailedReason    *types.FailureReason
}

// Tx is the transactional sub-interface exposed to InTx callbacks.
type Tx interface {
	Get(id types.TaskID) (TaskState, error)
	ListChildren(parent types.TaskID) ([]TaskState, error)
	ListByStatus(team types.TeamID, st types.Status, limit int) ([]TaskState, error)
	CAS(id types.TaskID, expect, set types.Status, mut Mutation) error
	Insert(ts TaskState) error
	Delete(id types.TaskID) error
}

// Store is the persistence boundary for tstate.
// Defined here (not in tstate/store) to avoid import cycles.
type Store interface {
	CAS(ctx context.Context, id types.TaskID, expect, set types.Status, mut Mutation) error
	Insert(ctx context.Context, ts TaskState) error
	Delete(ctx context.Context, id types.TaskID) error
	UpdateField(ctx context.Context, id types.TaskID, field string, value any, attemptGuard int) error
	Get(ctx context.Context, id types.TaskID) (TaskState, error)
	ListByStatus(ctx context.Context, team types.TeamID, st types.Status, limit int) ([]TaskState, error)
	ListByParent(ctx context.Context, parent types.TaskID) ([]TaskState, error)
	ListPendingDependentOn(ctx context.Context, depID types.TaskID) ([]TaskState, error)
	InTx(ctx context.Context, fn func(Tx) error) error
	Close() error
}
