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
	agentRunID, _ := sessionstats.AgentRunIDFromCtx(ctx)
	tracker := p.registry.GetOrCreate(sessionID)

	used, limit, history, toolResults, system := classifyContext(req)
	started := time.Now()

	stream, err := p.inner.Chat(ctx, req)
	if err != nil {
		// Even on dial failure record a call attempt with nil usage so
		// the LLMCalls counter reflects reality.
		tracker.RecordLLMCall(req.Model, agentRunID, nil, time.Since(started).Milliseconds())
		tracker.UpdateContextWindow(used, limit, history, toolResults, system)
		return nil, err
	}

	return wrapStream(stream, func(usage *types.Usage) {
		latencyMs := time.Since(started).Milliseconds()
		tracker.RecordLLMCall(req.Model, agentRunID, usage, latencyMs)

		// Prefer the model's reported input token count when present so
		// "used" is exact. The cheap estimate is kept only for the
		// history/tool_results/system split, which the model doesn't
		// give us.
		actualUsed := used
		if usage != nil && usage.InputTokens > 0 {
			actualUsed = int64(usage.InputTokens)
		}
		tracker.UpdateContextWindow(actualUsed, limit, history, toolResults, system)
	}), nil
}

// CountTokens passes through to the inner provider.
func (p *StatsProvider) CountTokens(ctx context.Context, messages []types.Message) (int, error) {
	return p.inner.CountTokens(ctx, messages)
}

// Name passes through to the inner provider.
func (p *StatsProvider) Name() string { return p.inner.Name() }
