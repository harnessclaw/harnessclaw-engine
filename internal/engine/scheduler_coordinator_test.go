package engine_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

type engineFakeSpawner struct{ output string }

func (f *engineFakeSpawner) SpawnSync(_ context.Context, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return &agent.SpawnResult{Output: f.output}, nil
}

func TestSchedulerCoordinator_RunLeafWithCutover(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "cutover done"}

	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	sp := spec.TaskSpec{
		Goal:      "test goal",
		Layout:    "flat",
		SessionID: "sess-cutover",
	}
	ref, err := sc.Run(ctx, sp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == "" {
		t.Fatal("expected non-empty MetaRef")
	}
}

func TestSchedulerCoordinator_RunLeaf_ReturnsMetaRef(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "task done"}

	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	sp := spec.TaskSpec{
		Goal:      "write hello world",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-test",
		Layout:    "flat",
	}
	ref, err := sc.Run(ctx, sp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == "" {
		t.Fatal("expected non-empty MetaRef")
	}
}
