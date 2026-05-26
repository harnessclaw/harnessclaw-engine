package plan

import (
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func buildPlanExecutorAgentSpec(parentGoal, sessionID string) spec.TaskSpec {
	return spec.TaskSpec{
		LocalID:      "plan-executor-agent",
		Goal:         "Execute plan for: " + parentGoal,
		Hint:         spec.Hint{Kind: types.KindLeaf},
		Layout:       "flat",
		SessionID:    sessionID,
		SubagentType: "plan-executor-agent",
	}
}
