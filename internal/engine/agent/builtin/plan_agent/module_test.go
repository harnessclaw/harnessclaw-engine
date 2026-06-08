package plan_agent_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/plan_agent"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/tools"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := plan_agent.New(plan_agent.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "plan_agent" {
		t.Errorf("SubagentType = %q, want plan_agent", got)
	}
}
