package builtin

import "harnessclaw-go/internal/legacy/agent"

// PlanDesign is the design / methodology planning sub-agent (the "Plan"
// user-facing role — NOT the plan-mode plan_agent). Drives the
// PlanProfile system prompt, strips dispatch tools (strict L3 leaf),
// terminates on natural end_turn, defaults to 15 turns.
//
// The "plan" Name doubles as the SubagentType the legacy module pinned
// into the prompt / events block; runner.Input.SubagentTypeOverride is
// stamped to this string by the shim.
var PlanDesign = agent.AgentDefinition{
	Name:        "plan",
	DisplayName: "Plan Designer",
	Description: "Designs approaches and methodologies without writing plan.json.",
	Profile:     "plan", // → prompt.PlanProfile
	MaxTurns:    15,
	IsBuiltin:   true,
}
