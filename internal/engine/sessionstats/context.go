// Package sessionstats provides in-memory per-session metric aggregation
// for the session metrics dashboard. See
// docs/superpowers/specs/2026-05-12-session-metrics-design.md.
package sessionstats

import "context"

type ctxKey int

const (
	ctxKeySessionID ctxKey = iota
	ctxKeyAgentRunID
)

// WithSessionID attaches a session ID to ctx. The Provider decorator
// reads it to route every Chat() call's usage to the correct tracker.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ctxKeySessionID, sessionID)
}

// SessionIDFromCtx extracts the session ID. Returns ("", false) when
// absent — the decorator treats that as "no tracking for this call".
func SessionIDFromCtx(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeySessionID).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// WithAgentRunID attaches an agent_run_id. The L1 main loop and each
// sub-agent goroutine override this before calling the provider so the
// decorator can attribute token usage to the right sub-agent row.
func WithAgentRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, ctxKeyAgentRunID, runID)
}

// AgentRunIDFromCtx extracts the agent_run_id. Returns ("", false)
// when absent.
func AgentRunIDFromCtx(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyAgentRunID).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
