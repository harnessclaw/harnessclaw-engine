package tstate

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// Writer is the only path to mutate TaskState status fields.
// HELD ONLY BY scheduler.go and handlers/ — never injected into dispatch.Deps.
type Writer interface {
	// Creation / control (scheduler top-level calls)
	Admit(ctx context.Context, sp spec.TaskSpec) (types.TaskID, error)
	Derive(ctx context.Context, parent types.TaskID, sp spec.TaskSpec) (types.TaskID, error)
	Cancel(ctx context.Context, id types.TaskID) error

	// RollbackAdmit (v3.1-R4/R10): physically delete a pending row.
	// Used by Submit when MarkReady/StartStrategyHost fails after Admit,
	// and by onSpawn saga when a later Derive fails. NOT a state-machine transition.
	RollbackAdmit(ctx context.Context, id types.TaskID) error

	// Runtime (called by handlers / reaper via scheduler)
	MarkReady(ctx context.Context, id types.TaskID) error
	Claim(ctx context.Context, id types.TaskID, worker string, lease time.Duration, attempt int) error
	RenewLease(ctx context.Context, id types.TaskID, worker string) error
	Park(ctx context.Context, id types.TaskID, waitingFor []types.TaskID) error
	Resume(ctx context.Context, id types.TaskID) error
	Succeed(ctx context.Context, id types.TaskID, ref types.Ref) error
	FailOrRetry(ctx context.Context, id types.TaskID, reason types.FailureReason, errMsg string, attempt int) error
	Expire(ctx context.Context, id types.TaskID, reason types.FailureReason, attempt int) error
	ConfirmCancelled(ctx context.Context, id types.TaskID) error
	ConfirmSucceededFromStaging(ctx context.Context, id types.TaskID, ref types.Ref, attempt int) error
}

// Kernel is Reader + Writer — held only by scheduler.go top-level.
type Kernel interface {
	Reader
	Writer
}
