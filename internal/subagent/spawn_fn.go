package subagent

import (
	"context"

	"harnessclaw-go/internal/agent"
)

// SpawnFn executes one sub-agent and returns its result.
// Injected into LeafContext by QueryEngineFactory; nil = stub mode.
type SpawnFn func(ctx context.Context) (*agent.SpawnResult, error)
