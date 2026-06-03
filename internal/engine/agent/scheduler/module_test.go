package scheduler_test

import (
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/scheduler"
)

func TestModule_SubagentTypeKey(t *testing.T) {
	m := scheduler.New(scheduler.Deps{
		Logger: zap.NewNop(),
	})
	if got := m.SubagentType(); got != "scheduler" {
		t.Errorf("SubagentType = %q, want scheduler", got)
	}
}
