package engine

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// retryRoutingKey is the private ctx-value key carrying the data
// retryLLMCall needs to attribute its heartbeat / retry events to the
// correct card on the wire. Planner and SubagentResolver both run
// outside the L1/L3 sub-agent driver loops that normally supply these
// fields directly to retryLLMCall — they fish them out of ctx instead.
//
// Plumbed via ctx (not function args) so adding fields here later
// doesn't ripple through every Planner / SubagentResolver
// implementation signature.
type retryRoutingKey struct{}

// retryRouting carries the per-call wire-routing info: which agent card
// the events should attach to (AgentID) and where to send them (Out).
// Both fields are optional — retryLLMCall handles zero values gracefully
// (heartbeat falls back to message card, retry note silently drops).
type retryRouting struct {
	AgentID string
	Out     chan<- types.EngineEvent
}

// WithRetryRouting returns a child ctx that carries agentID + out so
// downstream retryLLMCall invocations (in planner / resolver / any
// future LLM-calling helper that isn't part of the standard sub-agent
// driver) can route heartbeats and retry-status events to the right
// card. Pass-through for empty values: callers don't have to special-
// case "I don't know the agent_id yet".
func WithRetryRouting(ctx context.Context, agentID string, out chan<- types.EngineEvent) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, retryRoutingKey{}, retryRouting{
		AgentID: agentID,
		Out:     out,
	})
}

// retryRoutingFromCtx extracts the wire-routing pair set by
// WithRetryRouting. Returns zero values when nothing was plumbed —
// retryLLMCall is fine with empty agentID and nil out, so callers
// don't need to gate.
func retryRoutingFromCtx(ctx context.Context) (string, chan<- types.EngineEvent) {
	if ctx == nil {
		return "", nil
	}
	v, _ := ctx.Value(retryRoutingKey{}).(retryRouting)
	return v.AgentID, v.Out
}
