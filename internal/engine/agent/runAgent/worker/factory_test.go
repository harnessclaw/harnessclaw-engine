package worker_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/worker"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

type fakeSpawner struct {
	lastCfg *agent.SpawnConfig
}

func (f *fakeSpawner) SpawnSync(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	f.lastCfg = cfg
	return &agent.SpawnResult{Output: "stub-output"}, nil
}

var _ agent.AgentSpawner = (*fakeSpawner)(nil)

func TestFactory_Build_SetsSpawnFn(t *testing.T) {
	dir := t.TempDir()
	spawner := &fakeSpawner{}
	factory := worker.NewFactory(spawner, dir, "root-sess")
	sp := spec.TaskSpec{Goal: "write tests", SessionID: "root-sess", Model: "claude-3-5-haiku"}
	lc := factory.Build(types.TaskID("task-abc"), "root-sess", sp)
	if lc.SpawnFn == nil {
		t.Fatal("expected SpawnFn to be set by factory")
	}
	if lc.TaskID != "task-abc" {
		t.Fatalf("TaskID mismatch: got %q", lc.TaskID)
	}
	if lc.Workspace == nil {
		t.Fatal("expected Workspace to be set")
	}
}

func TestFactory_Build_SpawnFnCallsSpawnSync(t *testing.T) {
	dir := t.TempDir()
	spawner := &fakeSpawner{}
	factory := worker.NewFactory(spawner, dir, "sess-1")
	sp := spec.TaskSpec{
		Goal: "research llm", SessionID: "sess-1", Model: "claude-3-5-sonnet",
	}
	lc := factory.Build(types.TaskID("task-xyz"), "sess-1", sp)
	result, err := lc.SpawnFn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "stub-output" {
		t.Fatalf("want 'stub-output', got %q", result.Output)
	}
	if spawner.lastCfg == nil {
		t.Fatal("SpawnSync was not called")
	}
	if spawner.lastCfg.Prompt != "research llm" {
		t.Fatalf("Prompt mismatch: want %q, got %q", "research llm", spawner.lastCfg.Prompt)
	}
}
