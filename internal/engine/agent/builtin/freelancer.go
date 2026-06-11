package builtin

import "harnessclaw-go/internal/engine/agent/definition"

// Freelancer is the user-skill-driven L3 sub-agent. Capability is
// determined by AllowedSkills loaded at spawn time; the freelancer
// module shim hydrates SKILL.md content into the prompt before the
// loop starts. Drives the FreelancerProfile system prompt, strips
// dispatch tools (strict L3 leaf), default 20 turns. The terminal
// hook is built per-spawn from cfg.ExpectedOutputs +
// ContractEnforcerWithLogger; runner.Input.HookFactory carries it.
var Freelancer = definition.AgentDefinition{
	Name:        "freelancer",
	DisplayName: "Freelancer",
	Description: "Skill-driven L3 worker with contract-enforced submit_task_result.",
	Profile:     "freelancer", // → prompt.FreelancerProfile
	MaxTurns:    20,
	IsBuiltin:   true,
}
