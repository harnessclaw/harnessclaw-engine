package plan_test

import (
	"testing"

	schedulerplan "harnessclaw-go/internal/engine/scheduler/dispatch/plan"
)

func TestFallbackAggregator_EmptySteps(t *testing.T) {
	agg := schedulerplan.NewFallbackAggregator()
	out := agg.Aggregate(schedulerplan.FallbackInput{
		Goal:   "do something",
		Reason: "budget exhausted",
	})
	if out.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestFallbackAggregator_PartialResults(t *testing.T) {
	agg := schedulerplan.NewFallbackAggregator()
	out := agg.Aggregate(schedulerplan.FallbackInput{
		Goal:           "research and write",
		Reason:         "step failed",
		CompletedGoals: []string{"research phase done"},
		FailedGoals:    []string{"writing failed: timeout"},
	})
	if out.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if len(out.NeedsAttention) == 0 {
		t.Fatal("expected items in NeedsAttention for failed goals")
	}
}
