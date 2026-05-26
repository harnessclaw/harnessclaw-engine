package subagent_test

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/subagent"
)

func TestLeafContext_SpawnFnField(t *testing.T) {
	called := false
	fn := subagent.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
		called = true
		return &agent.SpawnResult{Output: "hello"}, nil
	})
	lc := subagent.LeafContext{SpawnFn: fn}
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
	lc := subagent.LeafContext{}
	if lc.SpawnFn != nil {
		t.Fatal("expected nil SpawnFn on zero LeafContext")
	}
}

func TestSpawnFnReturnsError(t *testing.T) {
	want := errors.New("boom")
	fn := subagent.SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
		return nil, want
	})
	_, got := fn(context.Background())
	if !errors.Is(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}
