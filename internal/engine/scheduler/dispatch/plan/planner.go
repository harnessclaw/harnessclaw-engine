// Package plan implements the multi-step plan dispatch strategy (§5.3.3).
// Phase 1 skeleton: planner and summarizer run as leaf sub-agents;
// real plan.json parsing and step expansion land in phase 2.
package plan

import (
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// buildPlannerSpec returns a leaf TaskSpec for the planner sub-agent.
// Layout=flat: writes sessionRoot/plan.json + meta.json.
func buildPlannerSpec(parentGoal, sessionID string) spec.TaskSpec {
	return spec.TaskSpec{
		LocalID:   "plan-planner",
		Goal:      "Generate execution plan for: " + parentGoal,
		Hint:      spec.Hint{Kind: types.KindLeaf},
		Layout:    "flat",
		SessionID: sessionID,
	}
}
