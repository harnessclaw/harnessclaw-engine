package tstate

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// StagingWriter writes ONLY the StagedResultRef field — non-state.
// Injected into runtime/host + subagent.Runner so L3 can persist its ResultRef
// before publishing lifecycle{completed}, providing the reaper fallback path.
type StagingWriter interface {
	StageResult(ctx context.Context, id types.TaskID, ref types.Ref, attempt int) error
}
