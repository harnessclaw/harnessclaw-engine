package builtin

import "harnessclaw-go/internal/legacy/agent"

// PlanAgent is the plan-mode planner sub-agent that breaks a task into
// plan.json and submits via submit_task_result. Drives the
// PlanAgentProfile system prompt, strips dispatch tools (no recursive
// spawning from inside the planner), defaults to 20 turns, terminates
// on submit_task_result via StopOnSubmitResult.
var PlanAgent = agent.AgentDefinition{
	Name:        "plan_agent",
	DisplayName: "Plan Agent",
	Description: "Plan-mode planner that produces plan.json via submit_task_result.",
	Profile:     "plan_agent", // → prompt.PlanAgentProfile
	MaxTurns:    20,
	IsBuiltin:   true,
}
