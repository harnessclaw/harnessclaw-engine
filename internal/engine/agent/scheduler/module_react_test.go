package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
)

// Both modes (react and plan) now delegate to agentrun.ModeScheduled —
// Module.Run must reject either mode when Deps.Runner is missing, with
// a clear error message rather than a nil deref panic.
func TestRun_RequiresRunner(t *testing.T) {
	for _, mode := range []string{"react", "plan"} {
		t.Run(mode, func(t *testing.T) {
			store := memory.New()
			mgr := session.NewManager(store, zap.NewNop(), time.Hour)
			m := scheduler.New(scheduler.Deps{
				Logger:     zap.NewNop(),
				SessionMgr: mgr,
				RootDir:    t.TempDir(),
				// Runner: nil — both modes must reject explicitly.
			})

			parentSess, _ := mgr.GetOrCreate(context.Background(), "parent-sess-"+mode, "ws", "user")
			cfg := &agent.SpawnConfig{
				Prompt:          "any goal",
				SubagentType:    "scheduler",
				ParentSessionID: parentSess.ID,
				RootSessionID:   parentSess.ID,
				CoordinatorMode: mode,
			}

			_, err := m.Run(context.Background(), cfg)
			if err == nil {
				t.Fatalf("expected error when %s mode used without Runner", mode)
			}
			if !strings.Contains(err.Error(), "requires Deps.Runner") {
				t.Errorf("error should mention Runner requirement, got: %v", err)
			}
		})
	}
}
