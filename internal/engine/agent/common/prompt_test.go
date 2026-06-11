package common_test

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/agent/definition"
)

func TestBuildSubAgentPrompt_FallbackWithNoBuilder(t *testing.T) {
	got := common.BuildSubAgentPrompt(common.PromptArgs{
		WorkerDisplayName: "freelancer",
	})
	if !strings.Contains(got, "freelancer") {
		t.Errorf("fallback prompt should mention worker name, got %q", got)
	}
}

func TestBuildSubAgentPrompt_FallbackPrefersSubagentType(t *testing.T) {
	got := common.BuildSubAgentPrompt(common.PromptArgs{
		SubagentType: "developer",
	})
	if !strings.Contains(got, "developer") {
		t.Errorf("fallback should mention subagent type when display name absent, got %q", got)
	}
}

func TestBuildSubAgentPrompt_FallbackWhenAllEmpty(t *testing.T) {
	got := common.BuildSubAgentPrompt(common.PromptArgs{})
	if got == "" {
		t.Error("fallback should never return an empty string")
	}
}

func TestBuildSubAgentPrompt_ComposesDefinitionSkillContractAndRenderedPrompt(t *testing.T) {
	def := &definition.AgentDefinition{
		Name:         "browser-agent",
		SystemPrompt: "AGENT SYSTEM PROMPT",
		Tier:         definition.TierSubAgent,
		OutputSchema: map[string]any{"type": "object"},
		Skills:       []string{"browser"},
	}

	got := common.BuildSubAgentPrompt(common.PromptArgs{
		AgentDef:          def,
		LoadedSkillsBlock: "<loaded-skills>OFFICIAL SKILL</loaded-skills>",
		WorkerDisplayName: "Browser Agent",
	})

	for _, want := range []string{
		"AGENT SYSTEM PROMPT",
		"<loaded-skills>OFFICIAL SKILL</loaded-skills>",
		"<sub-agent-contract>",
		"output_schema",
		"Browser Agent",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}
