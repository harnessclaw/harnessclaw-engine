package plan_design_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/plan_design"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/tool"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := plan_design.New(plan_design.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "plan" {
		t.Errorf("SubagentType = %q, want plan", got)
	}
}
