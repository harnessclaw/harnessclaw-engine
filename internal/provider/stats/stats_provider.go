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
	immediateParentID, _ := sessionstats.ImmediateParentSessionIDFromCtx(ctx)
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)

	// Three-layer write targets:
	//   - primary:         this agent's own tracker (always created)
	//   - immediate parent: the layer directly above (L2 for an L3 call)
	//   - root:            the user-facing emma session
	// We deduplicate so a layer that coincides with another doesn't get
	// the same call counted twice (e.g. an L2 where parent == root).
	trackers := []*sessionstats.Tracker{p.registry.GetOrCreate(sessionID)}
	seen := map[string]bool{sessionID: true}
	if immediateParentID != "" && !seen[immediateParentID] {
		trackers = append(trackers, p.registry.GetOrCreate(immediateParentID))
		seen[immediateParentID] = true
	}
	if rootSessionID != "" && !seen[rootSessionID] {
		trackers = append(trackers, p.registry.GetOrCreate(rootSessionID))
		seen[rootSessionID] = true
	}
	primaryTracker := trackers[0]

	used, limit, history, toolResults, system := classifyContext(req)
	started := time.Now()

	stream, err := p.inner.Chat(ctx, req)
	if err != nil {
		// Even on dial failure record a call attempt with nil usage so
		// the LLMCalls counter reflects reality. Update context window
		// only on the primary tracker — context budget is a per-session
		// concept (each session has its own composition); aggregating
		// multiple sessions' contexts into one limit would mislead.
		latencyMs := time.Since(started).Milliseconds()
		for _, tr := range trackers {
			tr.RecordLLMCall(req.Model, agentRunID, nil, latencyMs)
		}
		primaryTracker.UpdateContextWindow(used, limit, history, toolResults, system)
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
		for _, tr := range trackers {
			tr.RecordLLMCall(effectiveModel, agentRunID, usage, latencyMs)
		}

		// Prefer the model's reported input token count when present so
		// "used" is exact. The cheap estimate is kept only for the
		// history/tool_results/system split, which the model doesn't
		// give us. Only the primary tracker carries context-window data.
		actualUsed := used
		if usage != nil && usage.InputTokens > 0 {
			actualUsed = int64(usage.InputTokens)
		}
		primaryTracker.UpdateContextWindow(actualUsed, limit, history, toolResults, system)
	}), nil
}

// CountTokens passes through to the inner provider.
func (p *StatsProvider) CountTokens(ctx context.Context, messages []types.Message) (int, error) {
	return p.inner.CountTokens(ctx, messages)
}

// Name passes through to the inner provider.
func (p *StatsProvider) Name() string { return p.inner.Name() }
