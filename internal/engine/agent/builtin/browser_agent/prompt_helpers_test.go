package browser_agent

import (
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/skills"
)

// buildSystemPrompt constructs a representative Browser Agent system prompt
// through the shared sub-agent prompt assembler. Used only by prompt_test.go
// to verify prompt assembly.
func buildSystemPrompt() string {
	def := BrowserAgentDefinition()
	fakeSkill := &skill.SkillFull{
		SkillCard: skill.SkillCard{
			Name:    "agent-browser/core",
			Version: "embedded",
			Path:    "embedded://agent-browser/SKILL.md",
		},
		Body: adapterHeader + "\n\nOFFICIAL CORE SKILL BODY\n\nAlways call browser_agent_final_result when done.",
	}
	skillBlock := prompt.BuildLoadedSkillsBlock([]*skill.SkillFull{fakeSkill})
	return common.BuildSubAgentPrompt(common.PromptArgs{
		AgentDef:          def,
		LoadedSkillsBlock: skillBlock,
		WorkerDisplayName: def.DisplayName,
		SubagentType:      def.Name,
	})
}
