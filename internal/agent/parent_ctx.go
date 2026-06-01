package agent

import "context"

// parentAgentIDKey is the ctx key for the dispatching parent agent's
// session id. Sub-agent spawn paths read it to populate
// SpawnConfig.ParentAgentID when the caller hasn't set it explicitly —
// the translator then parents the L3 card under the L2 parent's card
// instead of falling back to the grandparent's tool card.
type parentAgentIDKey struct{}

// WithParentAgentID attaches the caller agent's id to ctx. L2 modules
// (scheduler, plan_executor_agent) call this before delegating to a
// Coordinator/strategy so the downstream QueryEngineFactory can stamp
// every L3 SpawnConfig with the correct ParentAgentID.
func WithParentAgentID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, parentAgentIDKey{}, id)
}

// ParentAgentIDFromCtx returns the parent agent id attached via
// WithParentAgentID, or "" when absent.
func ParentAgentIDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(parentAgentIDKey{}).(string)
	return id
}
