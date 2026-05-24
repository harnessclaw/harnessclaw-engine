package plan

import (
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// buildSummarizerSpec returns a leaf TaskSpec for the summarizer sub-agent.
// It receives the parent goal and the list of spawned step TaskIDs (phase 2 uses them).
func buildSummarizerSpec(parentGoal, sessionID string, _ []types.TaskID) spec.TaskSpec {
	return spec.TaskSpec{
		LocalID:   "plan-summarizer",
		Goal:      "Aggregate sub-results for: " + parentGoal,
		Hint:      spec.Hint{Kind: types.KindLeaf},
		Layout:    "flat",
		SessionID: sessionID,
	}
}
