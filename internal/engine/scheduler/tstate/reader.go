package tstate

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// Reader is the read-only view of tstate. All packages outside scheduler/handlers
// and scheduler.go itself must use only this interface (lint enforced).
type Reader interface {
	Get(ctx context.Context, id types.TaskID) (TaskState, error)
	ListReady(ctx context.Context, team types.TeamID, limit int) ([]TaskState, error)
	ListChildren(ctx context.Context, parent types.TaskID) ([]TaskState, error)
	ListByStatus(ctx context.Context, team types.TeamID, st types.Status, limit int) ([]TaskState, error)
	ListPendingDependentOn(ctx context.Context, depID types.TaskID) ([]TaskState, error)
}
