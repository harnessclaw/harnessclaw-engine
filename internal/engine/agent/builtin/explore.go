package builtin

import "harnessclaw-go/internal/legacy/agent"

// Explore is the read-only exploration sub-agent. Drives the
// ExploreProfile system prompt, strips dispatch tools (strict L3 leaf),
// terminates on natural end_turn, defaults to 10 turns.
//
// The "explore" Name is also the SubagentType the legacy module pinned
// into prompt / events; runner.Input.SubagentTypeOverride is stamped
// to this string by the shim.
var Explore = agent.AgentDefinition{
	Name:        "explore",
	DisplayName: "Explorer",
	Description: "Read-only investigation and exploration sub-agent.",
	Profile:     "explore", // → prompt.ExploreProfile
	MaxTurns:    10,
	IsBuiltin:   true,
}
