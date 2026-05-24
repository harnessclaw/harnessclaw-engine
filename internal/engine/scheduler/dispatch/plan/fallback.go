// Package plan implements L2 planning logic, split across fallback (graceful degradation),
// judgment (rule validation), and strategy/core decision-making.
package plan

import "fmt"

// FallbackInput is the structured input to FallbackAggregator.Aggregate,
// capturing the goal and reason for fallback, plus partial progress.
type FallbackInput struct {
	Goal           string
	Reason         string
	CompletedGoals []string
	FailedGoals    []string
}

// FallbackOutput is the result of graceful degradation aggregation.
type FallbackOutput struct {
	Summary        string
	NeedsAttention []string
}

// FallbackAggregator implements graceful degradation logic from coordinator_fallback.go,
// summarizing partial results when a plan is interrupted.
type FallbackAggregator struct{}

// NewFallbackAggregator creates a new FallbackAggregator.
func NewFallbackAggregator() *FallbackAggregator { return &FallbackAggregator{} }

// Aggregate builds a summary and attention list from partial plan execution.
func (f *FallbackAggregator) Aggregate(in FallbackInput) FallbackOutput {
	var summary string
	if len(in.CompletedGoals) > 0 {
		summary = fmt.Sprintf("Goal: %s\nPartially completed (%d steps done) before %s.\nCompleted: %v",
			in.Goal, len(in.CompletedGoals), in.Reason, in.CompletedGoals)
	} else {
		summary = fmt.Sprintf("Goal: %s\nFailed to complete: %s", in.Goal, in.Reason)
	}
	needs := make([]string, 0, len(in.FailedGoals))
	needs = append(needs, in.FailedGoals...)
	return FallbackOutput{Summary: summary, NeedsAttention: needs}
}
