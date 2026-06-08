package freelancer_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/freelancer"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/tools"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := freelancer.New(freelancer.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "freelancer" {
		t.Errorf("SubagentType = %q, want freelancer", got)
	}
}
