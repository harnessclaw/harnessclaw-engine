package common_test

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
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
