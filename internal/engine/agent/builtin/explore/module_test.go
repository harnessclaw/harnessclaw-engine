package explore_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/explore"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/tools"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := explore.New(explore.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "explore" {
		t.Errorf("SubagentType = %q, want explore", got)
	}
}
