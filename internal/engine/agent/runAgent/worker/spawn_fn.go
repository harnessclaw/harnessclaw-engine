package worker

import (
	"context"

	"harnessclaw-go/internal/legacy/agent"
)

// SpawnFn executes one sub-agent and returns its result.
// Injected into LeafContext by Factory; nil = stub mode.
type SpawnFn func(ctx context.Context) (*agent.SpawnResult, error)
