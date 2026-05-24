package engine_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestSchedulerCoordinator_Integration_FakeSpawner(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "integration done"}

	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sc.Start(ctx)

	for _, kind := range []types.Kind{types.KindReact} {
		sp := spec.TaskSpec{
			Goal:      "integration: " + string(kind),
			Hint:      spec.Hint{Kind: kind},
			SessionID: "sess-int",
			Layout:    "flat",
		}
		ref, err := sc.RunLeaf(ctx, "sess-int", sp)
		if err != nil {
			t.Fatalf("kind=%s RunLeaf error: %v", kind, err)
		}
		if ref == "" {
			t.Fatalf("kind=%s empty MetaRef", kind)
		}
	}
}
