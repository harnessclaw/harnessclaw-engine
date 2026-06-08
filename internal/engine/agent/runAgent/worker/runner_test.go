package worker_test

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/worker"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/legacy/workspace"
)

// stubWorkspace is a test double for worker.WorkspaceHandle.
type stubWorkspace struct {
	taskDir     string
	writtenMeta workspace.Meta
	metaCalled  bool
}

func (s *stubWorkspace) TaskDir() string                                       { return s.taskDir }
func (s *stubWorkspace) MetaPath() string                                      { return s.taskDir + "/meta.json" }
func (s *stubWorkspace) MetaRelPath() string                                   { return "meta.json" }
func (s *stubWorkspace) ReadScope() []string                                   { return nil }
func (s *stubWorkspace) WriteScope() []string                                  { return nil }
func (s *stubWorkspace) InputPaths() []string                                  { return nil }
func (s *stubWorkspace) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }
func (s *stubWorkspace) ReadFile(_ context.Context, _ string) ([]byte, error)  { return nil, nil }
func (s *stubWorkspace) WriteMeta(_ context.Context, m workspace.Meta) (string, error) {
	s.writtenMeta = m
	s.metaCalled = true
	return "meta.json", nil
}

// TestRunner_CallsSpawnFnWhenSet verifies that Run delegates to SpawnFn
// and writes meta.json when SpawnFn is set.
func TestRunner_CallsSpawnFnWhenSet(t *testing.T) {
	ws := &stubWorkspace{taskDir: t.TempDir()}
	spawnCalled := false

	lc := worker.LeafContext{
		TaskID:    types.TaskID("task-1"),
		SpecRef:   spec.TaskSpec{Goal: "do something"},
		Workspace: ws,
		SpawnFn: worker.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
			spawnCalled = true
			return &agent.SpawnResult{Output: "task output"}, nil
		}),
	}

	runner := worker.NewRunner(lc, "test-consumer")
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
	ws := &stubWorkspace{taskDir: t.TempDir()}

	lc := worker.LeafContext{
		TaskID:    types.TaskID("task-2"),
		SpecRef:   spec.TaskSpec{Goal: "fallback goal"},
		Workspace: ws,
		SpawnFn:   nil,
	}

	runner := worker.NewRunner(lc, "test-consumer")
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
	ws := &stubWorkspace{taskDir: t.TempDir()}
	want := errors.New("spawn failure")

	lc := worker.LeafContext{
		TaskID:    types.TaskID("task-3"),
		SpecRef:   spec.TaskSpec{Goal: "boom"},
		Workspace: ws,
		SpawnFn: worker.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
			return nil, want
		}),
	}

	runner := worker.NewRunner(lc, "test-consumer")
	_, err := runner.Run(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("want error %v, got %v", want, err)
	}
}
