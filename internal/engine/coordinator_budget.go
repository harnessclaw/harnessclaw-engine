package engine

import (
	"fmt"
	"sync"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/pkg/types"
)

// BudgetLimit declares the upper bounds an L2 task is allowed to consume.
// Zero in any field means "no limit on this dimension" — useful in tests
// and for legacy callers that haven't opted in to budget enforcement.
type BudgetLimit struct {
	// MaxTokens caps cumulative input + output tokens across every LLM call
	// the coordinator (and its sub-agents) make. Hard limit — when exceeded
	// the coordinator must short-circuit to FallbackChain.
	MaxTokens int

	// MaxDuration is the wall-clock cap from BudgetTracker.Start to now.
	// Counted independently from MaxTokens so a task that hangs on a slow
	// upstream is interrupted even if it hasn't burned token budget.
	MaxDuration time.Duration

	// MaxFailures bounds consecutive sub-agent failures (contract miss /
	// terminal_error). Hitting it triggers escalation or fallback.
	MaxFailures int

	// MaxLLMCalls limits the raw count of LLM round-trips, not tokens.
	// Useful when each call is bounded but the coordinator is mis-looping
	// (e.g., repeated re-plans). Zero means "use MaxTokens only".
	MaxLLMCalls int
}

// BudgetSnapshot is a point-in-time view of consumption used by telemetry
// and Fallback decisions.
type BudgetSnapshot struct {
	TokensUsed   int
	LLMCalls     int
	Failures     int
	Elapsed      time.Duration
	Limit        BudgetLimit
	Exceeded     bool
	ExceededWhy  string
}

// BudgetTracker accumulates cost across one L2 task. Concurrent-safe: every
// running L3 sub-agent reports usage back through the same tracker, and
// multiple goroutines (Scheduler micro-decision + parallel L3 dispatches)
// may update simultaneously.
//
// The tracker is the single source of truth for "are we still within
// budget?" — Coordinator code consults Snapshot() / Exceeded() rather than
// keeping its own counters. This keeps any future budget rule (per-tier
// caps, per-skill caps) implementable in one place.
type BudgetTracker struct {
	limit BudgetLimit
	start time.Time

	mu       sync.Mutex
	tokens   int
	llmCalls int
	failures int
}

// NewBudgetTracker constructs a tracker. Calling Start() locks in the
// "elapsed = now - start" anchor; callers must invoke it before the first
// LLM round-trip.
func NewBudgetTracker(limit BudgetLimit) *BudgetTracker {
	return &BudgetTracker{limit: limit}
}

// Start anchors the elapsed-time clock. Subsequent calls reset the anchor
// — used in tests; production callers Start once.
func (t *BudgetTracker) Start() *BudgetTracker {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.start = time.Now()
	return t
}

// AddUsage accumulates input/output tokens from a completed LLM call and
// bumps the LLMCalls counter by one. Pass nil to skip token accounting
// (e.g., when a stream errored before usage was reported).
func (t *BudgetTracker) AddUsage(u *types.Usage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.llmCalls++
	if u != nil {
		t.tokens += u.InputTokens + u.OutputTokens
	}
}

// NoteFailure records a sub-agent contract violation or terminal error.
// Used by the failure-cap rule (MaxFailures) and by FallbackChain to
// decide whether to keep retrying or surrender.
func (t *BudgetTracker) NoteFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures++
}

// Snapshot returns a copy of current consumption + exceeded status.
// Callers branch on Exceeded; ExceededWhy carries the reason for logs.
func (t *BudgetTracker) Snapshot() BudgetSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := BudgetSnapshot{
		TokensUsed: t.tokens,
		LLMCalls:   t.llmCalls,
		Failures:   t.failures,
		Limit:      t.limit,
	}
	if !t.start.IsZero() {
		snap.Elapsed = time.Since(t.start)
	}
	snap.Exceeded, snap.ExceededWhy = t.checkLocked(snap)
	return snap
}

// Exceeded is the fast-path predicate. Returns true and a short reason
// string the coordinator can log / surface in trace events.
func (t *BudgetTracker) Exceeded() (bool, string) {
	snap := t.Snapshot()
	return snap.Exceeded, snap.ExceededWhy
}

// checkLocked must be called with t.mu held. Order of checks is the order
// of operator priority — token blowups beat duration which beats failures
// which beats raw call count, because token overruns are the most expensive.
func (t *BudgetTracker) checkLocked(snap BudgetSnapshot) (bool, string) {
	if t.limit.MaxTokens > 0 && snap.TokensUsed > t.limit.MaxTokens {
		return true, fmt.Sprintf("token budget exhausted: used %d > %d", snap.TokensUsed, t.limit.MaxTokens)
	}
	if t.limit.MaxDuration > 0 && snap.Elapsed > t.limit.MaxDuration {
		return true, fmt.Sprintf("duration budget exhausted: elapsed %s > %s", snap.Elapsed.Truncate(time.Millisecond), t.limit.MaxDuration)
	}
	if t.limit.MaxFailures > 0 && snap.Failures >= t.limit.MaxFailures {
		return true, fmt.Sprintf("failure budget exhausted: %d >= %d", snap.Failures, t.limit.MaxFailures)
	}
	if t.limit.MaxLLMCalls > 0 && snap.LLMCalls >= t.limit.MaxLLMCalls {
		return true, fmt.Sprintf("LLM call budget exhausted: %d >= %d", snap.LLMCalls, t.limit.MaxLLMCalls)
	}
	return false, ""
}

// DefaultPlanBudget returns the limit applied to Plan-mode tasks when the
// caller doesn't supply their own. Picked conservatively — generous enough
// for genuine multi-step work, stingy enough that an unbounded re-plan
// loop trips early.
func DefaultPlanBudget() BudgetLimit {
	return BudgetLimit{
		MaxTokens:   200_000,
		MaxDuration: 10 * time.Minute,
		MaxFailures: 5,
		MaxLLMCalls: 30,
	}
}

// budgetSnapshotToSpent converts the engine-internal BudgetSnapshot into
// the wire-shape BudgetSpent that lives on agent.SpawnResult. Lives in
// this file (engine package) so the agent package stays free of any
// engine import.
func budgetSnapshotToSpent(s BudgetSnapshot) agentBudgetSpent {
	return agentBudgetSpent{
		TokensUsed:  s.TokensUsed,
		LLMCalls:    s.LLMCalls,
		Failures:    s.Failures,
		ElapsedMs:   s.Elapsed.Milliseconds(),
		Exceeded:    s.Exceeded,
		ExceededWhy: s.ExceededWhy,
	}
}

// agentBudgetSpent is a local type alias for agent.BudgetSpent. Defined
// at package level so the converter signature stays self-contained
// without forcing every caller to import the agent package just to read
// the wire shape.
type agentBudgetSpent = agent.BudgetSpent
