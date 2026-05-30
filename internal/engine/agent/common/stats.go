package common

import (
	"context"

	"harnessclaw-go/internal/engine/sessionstats"
)

// WithSubAgentStats injects the four stats ctx keys a sub-agent needs
// for token attribution: its own session_id, its agent_run_id, its
// immediate parent's session_id (for dual-write), and the root
// session_id (for multi-level rollup).
//
// rootSessionID may be empty; when empty, immediateParentSessionID is
// used as the root (matches the L2-spawned-by-emma case).
func WithSubAgentStats(ctx context.Context, ownSessionID, agentRunID, immediateParentSessionID, rootSessionID string) context.Context {
	ctx = sessionstats.WithSessionID(ctx, ownSessionID)
	ctx = sessionstats.WithAgentRunID(ctx, agentRunID)
	ctx = sessionstats.WithImmediateParentSessionID(ctx, immediateParentSessionID)
	if rootSessionID == "" {
		rootSessionID = immediateParentSessionID
	}
	ctx = sessionstats.WithRootSessionID(ctx, rootSessionID)
	return ctx
}
