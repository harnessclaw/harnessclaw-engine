package builtin

import "harnessclaw-go/internal/engine/agent/definition"

// PlanExecutor is the plan-mode executor sub-agent that drives the
// plan.json produced by PlanAgent — it dispatches freelancers via the
// freelance tool and integrates their results. Unlike most leaf agents
// it must KEEP the dispatch tools (so the shim sets
// StripDispatchTools=false explicitly). Terminates on
// submit_task_result; default 20 turns.
var PlanExecutor = definition.AgentDefinition{
	Name:        "plan_executor_agent",
	DisplayName: "Plan Executor",
	Description: "Plan-mode executor that dispatches freelancers per plan.json step.",
	Profile:     "plan_executor_agent", // → prompt.PlanExecutorAgentProfile
	MaxTurns:    20,
	IsBuiltin:   true,
}
