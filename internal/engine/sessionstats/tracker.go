package sessionstats

import (
	"sync"
	"time"

	"harnessclaw-go/pkg/types"
)

// Tracker is the per-session in-memory aggregator. One Tracker per
// active session; concurrent writes from multiple goroutines (main loop
// and parallel sub-agents) are protected by a single mutex.
//
// Tracker is the truth source while the session is in flight; the
// statsPersistWorker periodically flushes Snapshot() to the SQLite
// metrics_json column.
type Tracker struct {
	sessionID string

	mu        sync.Mutex
	stats     types.SessionStats
	perModel  map[string]*types.ModelStats
	subAgents map[string]*types.SubAgentStats
	subOrder  []string // preserves insertion order for stable Snapshot output

	notify chan<- struct{} // bound by Manager; non-blocking
}

// NewTracker constructs an empty Tracker for the given session.
func NewTracker(sessionID string) *Tracker {
	return &Tracker{
		sessionID: sessionID,
		stats:     types.SessionStats{SessionID: sessionID},
		perModel:  make(map[string]*types.ModelStats),
		subAgents: make(map[string]*types.SubAgentStats),
	}
}

// BindNotify wires the persist worker's notify channel. The Tracker
// sends a non-blocking signal on every write so the worker can debounce
// and flush. Pass nil to disconnect.
func (t *Tracker) BindNotify(ch chan<- struct{}) {
	t.mu.Lock()
	t.notify = ch
	t.mu.Unlock()
}

// RecordLLMCall accumulates one completed Chat() round. agentRunID may
// be empty for L1 main-loop calls; when non-empty AND a matching
// SubAgentStats row exists, the per-row counters are also updated.
//
// usage may be nil (stream errored before MessageEnd); we still bump
// LLMCalls and latency so the dashboard reflects the attempt.
//
// Note on thinking_tokens semantics: Anthropic reports reasoning tokens
// as a separate counter; OpenAI includes them inside completion_tokens.
// The Bifrost adapter (see internal/provider/bifrost/adapter.go) copies
// both verbatim — we accept them as the upstream reports them and do
// not subtract thinking from OutputTokens here. The dashboard's
// thinking_share thus reflects "of the reported output budget, how
// much was reasoning", which is what users want to see regardless of
// provider accounting convention.
func (t *Tracker) RecordLLMCall(model, agentRunID string, usage *types.Usage, latencyMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stats.LLMCalls++
	t.stats.LatencyMsTotal += latencyMs

	var in, out, cr, cw, think int64
	if usage != nil {
		in = int64(usage.InputTokens)
		out = int64(usage.OutputTokens)
		cr = int64(usage.CacheRead)
		cw = int64(usage.CacheWrite)
		think = int64(usage.ThinkingTokens)
	}

	t.stats.InputTokens += in
	t.stats.OutputTokens += out
	t.stats.CacheReadTokens += cr
	t.stats.CacheWriteTokens += cw
	t.stats.ThinkingTokens += think

	// Derived ratios (with division-by-zero guards).
	if t.stats.LLMCalls > 0 {
		t.stats.LatencyMsAvg = t.stats.LatencyMsTotal / int64(t.stats.LLMCalls)
	}
	if denom := t.stats.CacheReadTokens + t.stats.InputTokens; denom > 0 {
		t.stats.CacheHitRate = float64(t.stats.CacheReadTokens) / float64(denom)
	}
	if t.stats.OutputTokens > 0 {
		t.stats.ThinkingShare = float64(t.stats.ThinkingTokens) / float64(t.stats.OutputTokens)
	}

	// Per-model aggregation.
	if model != "" {
		m := t.perModel[model]
		if m == nil {
			m = &types.ModelStats{Model: model}
			t.perModel[model] = m
		}
		m.InputTokens += in
		m.OutputTokens += out
		m.CacheReadTokens += cr
		m.CacheWriteTokens += cw
		m.ThinkingTokens += think
		m.LLMCalls++
	}

	// Sub-agent attribution (if a row was opened via StartSubAgent).
	if sa, ok := t.subAgents[agentRunID]; ok && agentRunID != "" {
		sa.InputTokens += in
		sa.OutputTokens += out
		sa.CacheReadTokens += cr
		sa.CacheWriteTokens += cw
		sa.ThinkingTokens += think
		sa.TotalTokens = sa.InputTokens + sa.OutputTokens
		sa.LLMCalls++
		if sa.Model == "" {
			sa.Model = model
		} else if sa.Model != model && sa.Model != "mixed" {
			sa.Model = "mixed"
		}
	}

	t.stats.UpdatedAt = time.Now().UTC()
	t.kickNotifyLocked()
}

// Snapshot returns a deep copy of the current stats. Safe to mutate or
// JSON-marshal without holding the tracker lock.
func (t *Tracker) Snapshot() types.SessionStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *Tracker) snapshotLocked() types.SessionStats {
	out := t.stats

	out.PerModel = make([]types.ModelStats, 0, len(t.perModel))
	for _, m := range t.perModel {
		out.PerModel = append(out.PerModel, *m)
	}

	out.SubAgents = make([]types.SubAgentStats, 0, len(t.subOrder))
	for _, id := range t.subOrder {
		if sa := t.subAgents[id]; sa != nil {
			out.SubAgents = append(out.SubAgents, *sa)
		}
	}
	return out
}

// kickNotifyLocked fires the persist worker without blocking. Caller
// holds t.mu.
func (t *Tracker) kickNotifyLocked() {
	if t.notify == nil {
		return
	}
	select {
	case t.notify <- struct{}{}:
	default:
	}
}
