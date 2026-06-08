package generic_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/generic"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/tools"
)

// TestModule_SubagentTypeKey pins the conventional fallback key used by
// Spawner.SetFallback. Renaming or changing it is a breaking change for
// the spawn → generic wiring done in Stage 4.3.
func TestModule_SubagentTypeKey(t *testing.T) {
	m := generic.New(generic.Deps{
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Registry:      tool.NewRegistry(),
		Logger:        zap.NewNop(),
	})
	if got := m.SubagentType(); got != "__generic__" {
		t.Errorf("SubagentType = %q, want __generic__", got)
	}
}
