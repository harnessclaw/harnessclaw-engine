package store

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
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

// Store is the persistence boundary for tstate.
type Store interface {
	CAS(ctx context.Context, id types.TaskID, expect, set types.Status, mut Mutation) error
	Insert(ctx context.Context, ts tstate.TaskState) error
	Delete(ctx context.Context, id types.TaskID) error
	UpdateField(ctx context.Context, id types.TaskID, field string, value any, attemptGuard int) error
	Get(ctx context.Context, id types.TaskID) (tstate.TaskState, error)
	ListByStatus(ctx context.Context, team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error)
	ListByParent(ctx context.Context, parent types.TaskID) ([]tstate.TaskState, error)
	ListPendingDependentOn(ctx context.Context, depID types.TaskID) ([]tstate.TaskState, error)
	InTx(ctx context.Context, fn func(Tx) error) error
	Close() error
}

// Tx is the transactional sub-interface exposed to InTx callbacks.
type Tx interface {
	Get(id types.TaskID) (tstate.TaskState, error)
	ListChildren(parent types.TaskID) ([]tstate.TaskState, error)
	ListByStatus(team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error)
	CAS(id types.TaskID, expect, set types.Status, mut Mutation) error
	Insert(ts tstate.TaskState) error
	Delete(id types.TaskID) error
}
