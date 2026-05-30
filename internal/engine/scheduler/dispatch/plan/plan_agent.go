package plan

import (
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func buildPlanAgentSpec(parentGoal, sessionID string) spec.TaskSpec {
	return spec.TaskSpec{
		LocalID:      "plan_agent",
		Goal:         "Generate execution plan for: " + parentGoal,
		Hint:         spec.Hint{Kind: types.KindLeaf},
		Layout:       "flat",
		SessionID:    sessionID,
		SubagentType: "plan_agent",
	}
}
