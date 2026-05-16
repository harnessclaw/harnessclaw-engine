package stats

import (
	"context"
	"time"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// StatsProvider is a provider.Provider decorator that records every
// LLM call's usage into the session tracker keyed by ctx-attached
// session_id and agent_run_id.
type StatsProvider struct {
	inner    provider.Provider
	registry *sessionstats.Registry
}

// New wraps inner so all of its Chat() calls flow through the metrics
// tracker. CountTokens and Name pass through unchanged.
func New(inner provider.Provider, registry *sessionstats.Registry) *StatsProvider {
	return &StatsProvider{inner: inner, registry: registry}
}

// Chat invokes the inner provider and taps the resulting stream's
// MessageEnd to record token usage / latency / context-window data.
// When no session id is attached to ctx the call passes through
// untouched.
func (p *StatsProvider) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	sessionID, hasSession := sessionstats.SessionIDFromCtx(ctx)
	if !hasSession {
		return p.inner.Chat(ctx, req)
	}
	rootSessionID, _ := sessionstats.RootSessionIDFromCtx(ctx)
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	primaryTracker := p.registry.GetOrCreate(sessionID)
	// Root tracker: only created when root differs from the immediate parent
	// session, i.e. this is an L3+ sub-agent. When they are equal we already
	// write to primaryTracker and there is nothing to dual-write.
	var rootTracker *sessionstats.Tracker
	if rootSessionID != "" && rootSessionID != sessionID {
		rootTracker = p.registry.GetOrCreate(rootSessionID)
	}

	used, limit, history, toolResults, system := classifyContext(req)
	started := time.Now()

	stream, err := p.inner.Chat(ctx, req)
	if err != nil {
		// Even on dial failure record a call attempt with nil usage so
		// the LLMCalls counter reflects reality.
		primaryTracker.RecordLLMCall(req.Model, agentRunID, nil, time.Since(started).Milliseconds())
		primaryTracker.UpdateContextWindow(used, limit, history, toolResults, system)
		if rootTracker != nil {
			rootTracker.RecordLLMCall(req.Model, agentRunID, nil, time.Since(started).Milliseconds())
			// No UpdateContextWindow on root — context budget is a per-session
			// concept (each session has its own composition); aggregating
			// multiple sessions' contexts into one limit would mislead.
		}
		return nil, err
	}

	return wrapStream(ctx, stream, func(usage *types.Usage, streamModel string) {
		latencyMs := time.Since(started).Milliseconds()

		// Prefer the model reported by the provider's MessageEnd over
		// the (often empty) req.Model — engine call sites don't fill
		// req.Model, relying on the provider's configured default.
		// Falling back to req.Model preserves correctness for callers
		// that do set it.
		effectiveModel := streamModel
		if effectiveModel == "" {
			effectiveModel = req.Model
		}
		primaryTracker.RecordLLMCall(effectiveModel, agentRunID, usage, latencyMs)

		// Prefer the model's reported input token count when present so
		// "used" is exact. The cheap estimate is kept only for the
		// history/tool_results/system split, which the model doesn't
		// give us.
		actualUsed := used
		if usage != nil && usage.InputTokens > 0 {
			actualUsed = int64(usage.InputTokens)
		}
		primaryTracker.UpdateContextWindow(actualUsed, limit, history, toolResults, system)

		if rootTracker != nil {
			rootTracker.RecordLLMCall(effectiveModel, agentRunID, usage, latencyMs)
			// Skip UpdateContextWindow on root (see error path comment above).
		}
	}), nil
}

// CountTokens passes through to the inner provider.
func (p *StatsProvider) CountTokens(ctx context.Context, messages []types.Message) (int, error) {
	return p.inner.CountTokens(ctx, messages)
}

// Name passes through to the inner provider.
func (p *StatsProvider) Name() string { return p.inner.Name() }
