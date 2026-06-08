package emit

import "context"

// TraceContext bundles the per-trace state that needs to flow alongside
// a Go context.Context: which trace we're inside, and the shared
// Sequencer used to allocate seq numbers.
//
// Stored in context.Context via WithTrace / FromContext so deeply-nested
// callees (tools, sub-agents) can attach the same trace to events they
// emit without threading parameters through every function signature.
type TraceContext struct {
	TraceID       string
	ParentEventID string
	ParentTaskID  string
	Sequencer     *Sequencer
}

type traceCtxKey struct{}

// WithTrace returns a derived context carrying tc. Calling FromContext
// on the returned context returns tc; otherwise it returns nil.
func WithTrace(ctx context.Context, tc *TraceContext) context.Context {
	if tc == nil {
		return ctx
	}
	return context.WithValue(ctx, traceCtxKey{}, tc)
}

// FromContext returns the TraceContext attached to ctx, or nil if none.
// Callers must check for nil before dereferencing.
func FromContext(ctx context.Context) *TraceContext {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(traceCtxKey{}).(*TraceContext); ok {
		return v
	}
	return nil
}
