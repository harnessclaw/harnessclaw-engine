package scheduler_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/engine/agent/scheduler"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

func TestSchedulerCoordinator_Integration_FakeSpawner(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "integration done"}

	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)

	for _, kind := range []types.Kind{types.KindReact} {
		sp := spec.TaskSpec{
			Goal:      "integration: " + string(kind),
			Hint:      spec.Hint{Kind: kind},
			SessionID: "sess-int",
			Layout:    "flat",
		}
		res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
		if err != nil {
			t.Fatalf("kind=%s Run error: %v", kind, err)
		}
		if res.MetaRef == "" {
			t.Fatalf("kind=%s empty MetaRef", kind)
		}
	}
}
