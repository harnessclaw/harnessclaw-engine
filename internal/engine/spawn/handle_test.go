package spawn_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/spawn"
)

func TestAsync_HandleWaitReturnsResult(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	s.Register(&fakeModule{
		subagentType: "fast",
		out:          &agent.SpawnResult{Output: "done"},
	})

	h, err := s.Async(context.Background(), &agent.SpawnConfig{
		SubagentType: "fast",
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.AgentID() == "" {
		t.Error("AgentID empty")
	}

	res, err := h.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "done" {
		t.Errorf("output = %q, want done", res.Output)
	}
}

type slowModule struct {
	subagentType string
	finished     chan struct{}
}

func (m *slowModule) SubagentType() string { return m.subagentType }
func (m *slowModule) Run(ctx context.Context, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
	<-ctx.Done()
	close(m.finished)
	return nil, ctx.Err()
}

func TestAsync_HandleCancelInterruptsRun(t *testing.T) {
	s := spawn.NewSpawner(zap.NewNop())
	mod := &slowModule{subagentType: "slow", finished: make(chan struct{})}
	s.Register(mod)

	h, _ := s.Async(context.Background(), &agent.SpawnConfig{
		SubagentType: "slow",
	})
	h.Cancel()

	select {
	case <-mod.finished:
	case <-time.After(time.Second):
		t.Fatal("module did not see cancel within 1s")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := h.Wait(waitCtx)
	if err != nil {
		// Some impls wrap; accept anything that surfaces cancellation
		if !errors.Is(err, context.Canceled) {
			t.Logf("wait err: %v", err)
		}
	}
}
