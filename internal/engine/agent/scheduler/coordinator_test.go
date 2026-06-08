package scheduler_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/engine/agent/scheduler"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

type engineFakeSpawner struct{ output string }

func (f *engineFakeSpawner) SpawnSync(_ context.Context, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return &agent.SpawnResult{Output: f.output}, nil
}

func TestSchedulerCoordinator_RunLeafWithCutover(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "cutover done"}

	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)

	sp := spec.TaskSpec{
		Goal:      "test goal",
		Layout:    "flat",
		SessionID: "sess-cutover",
	}
	res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MetaRef == "" {
		t.Fatal("expected non-empty MetaRef")
	}
}

func TestSchedulerCoordinator_RunLeaf_ReturnsMetaRef(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "task done"}

	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)

	sp := spec.TaskSpec{
		Goal:      "write hello world",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-test",
		Layout:    "flat",
	}
	res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.MetaRef == "" {
		t.Fatal("expected non-empty MetaRef")
	}
}
