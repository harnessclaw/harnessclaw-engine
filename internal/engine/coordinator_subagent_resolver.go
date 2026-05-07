package engine

import (
	"context"
	"fmt"
	"strings"
)

// SubagentResolver decides which L3 sub-agent runs a given step at
// dispatch time. Replaces the v1.15- behaviour where the Planner pre-
// bound each step to a sub-agent type — the new model lets Plan focus
// on "what to do" while keeping "who does it" as a runtime concern.
//
// Implementations:
//   - HeuristicSubagentResolver: zero-LLM keyword matcher (default)
//   - LLMSubagentResolver: stub for a future structured-output LLM
//     decision; falls through to heuristic until wired
//
// The Scheduler calls Resolve before every step dispatch; if PlanStep.
// SubagentType was already set (e.g. by an LLM Planner that produced
// an executor-aware plan), the Scheduler short-circuits and uses it
// directly without consulting the resolver.
type SubagentResolver interface {
	// Resolve returns the sub-agent name to dispatch plus a short
	// rationale for telemetry. Must always return a name from
	// available — implementations fall back rather than fail when no
	// rule matches.
	//
	// goal is typically PlanStep.Prompt or PlanStep.Description; when
	// both are empty the implementation should fall back to a generic
	// agent (general-purpose / first available).
	Resolve(ctx context.Context, goal string, available []string) (subagent string, reason string, err error)
}

// HeuristicSubagentResolver applies the same keyword rules previously
// embedded in HeuristicPlanner. Pure function of (goal, available); no
// LLM, no I/O.
type HeuristicSubagentResolver struct{}

// NewHeuristicSubagentResolver returns the default resolver.
func NewHeuristicSubagentResolver() *HeuristicSubagentResolver {
	return &HeuristicSubagentResolver{}
}

// Resolve picks a sub-agent by keyword match against goal. Order is
// "more specific first" — developer keywords before writer's "写", etc.
// Falls through to general-purpose, then first-available.
func (r *HeuristicSubagentResolver) Resolve(_ context.Context, goal string, available []string) (string, string, error) {
	if len(available) == 0 {
		return "", "", fmt.Errorf("subagent resolver: no available sub-agents")
	}
	set := newSubagentSet(available)
	lower := strings.ToLower(strings.TrimSpace(goal))

	if lower == "" {
		// No goal text at all — pick the safest fallback.
		return fallbackSubagent(set, available), "fallback: empty goal", nil
	}

	// Specific-first ordering matters: developer keys overlap with
	// writer's "写" / "code" overlaps; we want the more specific
	// match to win.
	for _, candidate := range []string{
		"developer", "researcher", "analyst",
		"travel_planner", "recommender", "scheduler", "writer",
	} {
		if !set.has(candidate) {
			continue
		}
		if matchesSubagent(lower, candidate) {
			return candidate, fmt.Sprintf("matched keyword rule for %q", candidate), nil
		}
	}

	// Fallback path: general-purpose if available, else first available.
	pick := fallbackSubagent(set, available)
	return pick, "no keyword match; defaulting to fallback", nil
}

// fallbackSubagent encapsulates the "no rule matched" priority:
// general-purpose first (handles arbitrary tasks), then any.
func fallbackSubagent(set subagentSet, available []string) string {
	if set.has("general-purpose") {
		return "general-purpose"
	}
	return available[0]
}

// LLMSubagentResolver is the placeholder for an LLM-driven resolver.
// Until the prompt format stabilises, it delegates to a fallback
// (heuristic by default). Wiring an LLM call later only changes Resolve;
// the Scheduler-side contract stays identical.
type LLMSubagentResolver struct {
	fallback SubagentResolver
}

// NewLLMSubagentResolver wraps a fallback resolver. Pass nil to use
// the heuristic resolver as fallback.
func NewLLMSubagentResolver(fallback SubagentResolver) *LLMSubagentResolver {
	if fallback == nil {
		fallback = NewHeuristicSubagentResolver()
	}
	return &LLMSubagentResolver{fallback: fallback}
}

// Resolve currently delegates to fallback. The "(LLM stub) " prefix in
// the rationale tells observers which path actually picked the agent.
func (r *LLMSubagentResolver) Resolve(ctx context.Context, goal string, available []string) (string, string, error) {
	pick, reason, err := r.fallback.Resolve(ctx, goal, available)
	if err != nil {
		return "", "", err
	}
	return pick, "(LLM stub) " + reason, nil
}
