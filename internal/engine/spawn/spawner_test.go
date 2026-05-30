package spawn_test

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/spawn"
)

type fakeModule struct {
	subagentType string
	runCalled    int
	out          *agent.SpawnResult
	err          error
}

func (f *fakeModule) SubagentType() string { return f.subagentType }
func (f *fakeModule) Run(_ context.Context, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
	f.runCalled++
	return f.out, f.err
}

func TestRegister_AssignsKeyFromModule(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	mod := &fakeModule{subagentType: "fake"}
	s.Register(mod)

	res, err := s.Sync(context.Background(), &agent.SpawnConfig{
		SubagentType: "fake",
	})
	if err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if mod.runCalled != 1 {
		t.Errorf("module.Run called %d times, want 1", mod.runCalled)
	}
	_ = res
}

func TestRegister_DuplicatePanics(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	s.Register(&fakeModule{subagentType: "x"})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate register")
		}
	}()
	s.Register(&fakeModule{subagentType: "x"})
}

func TestSync_UnknownTypeWithoutFallback_ReturnsError(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	_, err := s.Sync(context.Background(), &agent.SpawnConfig{
		SubagentType: "nobody",
	})
	if !errors.Is(err, spawn.ErrUnknownSubagentType) {
		t.Errorf("err = %v, want ErrUnknownSubagentType", err)
	}
}

func TestSetFallback_UnknownTypeRoutesToFallback(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	fallback := &fakeModule{subagentType: "__fallback__",
		out: &agent.SpawnResult{Output: "fb"}}
	s.SetFallback(fallback)

	res, err := s.Sync(context.Background(), &agent.SpawnConfig{
		SubagentType: "nobody",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.runCalled != 1 {
		t.Errorf("fallback.Run not called")
	}
	if res.Output != "fb" {
		t.Errorf("output = %q, want fb", res.Output)
	}
}
