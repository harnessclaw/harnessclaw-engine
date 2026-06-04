package browser_agent

import (
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/skill"
)

// buildSystemPrompt constructs a representative system prompt using a fake
// skill provider. Used only by prompt_test.go to verify prompt assembly.
//
// When a SkillProvider is active the official skill block replaces the old
// def.SystemPrompt (which references legacy browser_navigate / browser_snapshot
// wrappers). The contract is still appended. The fake skill body includes the
// Browser Agent final-result wrapper to mirror HarnessClaw's adapted skill.
func buildSystemPrompt() string {
	def := agent.BrowserAgentDefinition()
	fakeSkill := &skill.SkillFull{
		SkillCard: skill.SkillCard{
			Name:    "agent-browser/core",
			Version: "embedded",
			Path:    "embedded://agent-browser/SKILL.md",
		},
		Body: adapterHeader + "\n\nOFFICIAL CORE SKILL BODY\n\nAlways call browser_agent_final_result when done.",
	}
	skillBlock := prompt.BuildLoadedSkillsBlock([]*skill.SkillFull{fakeSkill})
	return joinNonEmpty([]string{
		skillBlock,
		agent.RenderSubAgentContract(def),
	}, "\n\n")
}
