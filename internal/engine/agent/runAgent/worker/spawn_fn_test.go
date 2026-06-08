package worker_test

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/worker"
)

func TestLeafContext_SpawnFnField(t *testing.T) {
	called := false
	fn := worker.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
		called = true
		return &agent.SpawnResult{Output: "hello"}, nil
	})
	lc := worker.LeafContext{SpawnFn: fn}
	if lc.SpawnFn == nil {
		t.Fatal("expected SpawnFn to be set")
	}
	result, err := lc.SpawnFn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("SpawnFn was not called")
	}
	if result.Output != "hello" {
		t.Fatalf("want 'hello', got %q", result.Output)
	}
}

func TestLeafContext_SpawnFnNilSafe(t *testing.T) {
	lc := worker.LeafContext{}
	if lc.SpawnFn != nil {
		t.Fatal("expected nil SpawnFn on zero LeafContext")
	}
}

func TestSpawnFnReturnsError(t *testing.T) {
	want := errors.New("boom")
	fn := worker.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
		return nil, want
	})
	_, got := fn(context.Background())
	if !errors.Is(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}
