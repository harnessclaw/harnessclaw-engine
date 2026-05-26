package subagent_test

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/subagent"
	"harnessclaw-go/internal/workspace"
)

// stubWorkspace2 is a test double for subagent.WorkspaceHandle.
// Named with "2" suffix to avoid collision with any stub in runner_test.go.
type stubWorkspace2 struct {
	taskDir    string
	writtenMeta workspace.Meta
	metaCalled  bool
}

func (s *stubWorkspace2) TaskDir() string     { return s.taskDir }
func (s *stubWorkspace2) MetaPath() string    { return s.taskDir + "/meta.json" }
func (s *stubWorkspace2) MetaRelPath() string { return "meta.json" }
func (s *stubWorkspace2) ReadScope() []string  { return nil }
func (s *stubWorkspace2) WriteScope() []string { return nil }
func (s *stubWorkspace2) InputPaths() []string { return nil }
func (s *stubWorkspace2) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }
func (s *stubWorkspace2) ReadFile(_ context.Context, _ string) ([]byte, error)  { return nil, nil }
func (s *stubWorkspace2) WriteMeta(_ context.Context, m workspace.Meta) (string, error) {
	s.writtenMeta = m
	s.metaCalled = true
	return "meta.json", nil
}

// TestRunner_CallsSpawnFnWhenSet verifies that Run delegates to SpawnFn
// and writes meta.json when SpawnFn is set.
func TestRunner_CallsSpawnFnWhenSet(t *testing.T) {
	ws := &stubWorkspace2{taskDir: t.TempDir()}
	spawnCalled := false

	lc := subagent.LeafContext{
		TaskID:    types.TaskID("task-1"),
		SpecRef:   spec.TaskSpec{Goal: "do something"},
		Workspace: ws,
		SpawnFn: subagent.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
			spawnCalled = true
			return &agent.SpawnResult{Output: "task output"}, nil
		}),
	}

	runner := subagent.NewRunner(lc, "test-consumer")
	ref, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spawnCalled {
		t.Fatal("SpawnFn was not called")
	}
	if ref == "" {
		t.Fatal("expected non-empty MetaRef")
	}
	if !ws.metaCalled {
		t.Fatal("WriteMeta was not called after SpawnFn")
	}
	if ws.writtenMeta.Status != workspace.StatusDone {
		t.Fatalf("expected status %q, got %q", workspace.StatusDone, ws.writtenMeta.Status)
	}
}

// TestRunner_FallsBackToStubWhenSpawnFnNil verifies that Run still returns
// a non-empty ref when SpawnFn is nil (original stub behavior).
func TestRunner_FallsBackToStubWhenSpawnFnNil(t *testing.T) {
	ws := &stubWorkspace2{taskDir: t.TempDir()}

	lc := subagent.LeafContext{
		TaskID:    types.TaskID("task-2"),
		SpecRef:   spec.TaskSpec{Goal: "fallback goal"},
		Workspace: ws,
		SpawnFn:   nil,
	}

	runner := subagent.NewRunner(lc, "test-consumer")
	ref, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref == "" {
		t.Fatal("expected non-empty MetaRef from stub path")
	}
}

// TestRunner_SpawnFnErrorPropagates verifies that an error from SpawnFn
// is returned as-is from Run.
func TestRunner_SpawnFnErrorPropagates(t *testing.T) {
	ws := &stubWorkspace2{taskDir: t.TempDir()}
	want := errors.New("spawn failure")

	lc := subagent.LeafContext{
		TaskID:    types.TaskID("task-3"),
		SpecRef:   spec.TaskSpec{Goal: "boom"},
		Workspace: ws,
		SpawnFn: subagent.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
			return nil, want
		}),
	}

	runner := subagent.NewRunner(lc, "test-consumer")
	_, err := runner.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("want error %v, got %v", want, err)
	}
}
