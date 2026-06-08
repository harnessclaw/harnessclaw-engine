package plan_executor_agent_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/plan_executor_agent"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/tools"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := plan_executor_agent.New(plan_executor_agent.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "plan_executor_agent" {
		t.Errorf("SubagentType = %q, want plan_executor_agent", got)
	}
}
