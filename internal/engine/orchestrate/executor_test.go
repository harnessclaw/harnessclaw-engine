package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/pkg/types"
)

// recordingSpawner is a programmable AgentSpawner for executor tests.
//
// Behaviour:
//   - For each spawn, it looks up a per-step handler keyed by SpawnConfig.Name
//     (each Step's StepID is used as the spawned agent's Name in production).
//   - If no handler is registered, returns a default success.
//   - Tracks call counts and the order of calls for assertions.
type recordingSpawner struct {
	mu        sync.Mutex
	calls     []agent.SpawnConfig
	handlers  map[string]func(call int, cfg *agent.SpawnConfig) (*agent.SpawnResult, error)
	callCount map[string]int
	delay     time.Duration // optional per-spawn delay used by parallelism tests
}

func newRecordingSpawner() *recordingSpawner {
	return &recordingSpawner{
		handlers:  make(map[string]func(int, *agent.SpawnConfig) (*agent.SpawnResult, error)),
		callCount: make(map[string]int),
	}
}

func (s *recordingSpawner) on(name string, fn func(call int, cfg *agent.SpawnConfig) (*agent.SpawnResult, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[name] = fn
}

func (s *recordingSpawner) SpawnSync(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	s.callCount[cfg.Name]++
	call := s.callCount[cfg.Name]
	s.calls = append(s.calls, *cfg)
	fn, ok := s.handlers[cfg.Name]
	s.mu.Unlock()

	if ok {
		return fn(call, cfg)
	}
	// Default: success with empty summary.
	return &agent.SpawnResult{
		AgentID: "agent-" + cfg.Name,
		Output:  "<summary>done</summary>\n\nresult of " + cfg.Name,
		Summary: "done " + cfg.Name,
		Status:  "completed",
	}, nil
}

// successResult builds a canonical completed SpawnResult.
func successResult(name, summary string, deliverables ...types.Deliverable) *agent.SpawnResult {
	return &agent.SpawnResult{
		AgentID:      "agent-" + name,
		Output:       "<summary>" + summary + "</summary>",
		Summary:      summary,
		Status:       "completed",
		Deliverables: deliverables,
	}
}

func TestExecutor_SingleStepSuccess(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s1", "邮件已写好"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "writer", Task: "写邮件"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusCompleted {
		t.Fatalf("status = %s, want %s", res.Status, StatusCompleted)
	}
	if len(res.Steps) != 1 || res.Steps[0].Status != StepStatusCompleted {
		t.Errorf("step results: %+v", res.Steps)
	}
	if res.Steps[0].Summary != "邮件已写好" {
		t.Errorf("summary = %q", res.Steps[0].Summary)
	}
}

func TestExecutor_ContextPropagation(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s1", "A 营收 $2B"), nil
	})
	var s2Ctx string
	sp.on("s2", func(_ int, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
		s2Ctx = cfg.ContextSummary
		return successResult("s2", "对比已完成"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "researcher", Task: "查竞品"},
		{StepID: "s2", SubagentType: "analyst", Task: "对比", DependsOn: []string{"s1"}},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
	if !strings.Contains(s2Ctx, "A 营收 $2B") {
		t.Errorf("s2 ContextSummary should contain s1 summary; got %q", s2Ctx)
	}
	if !strings.Contains(s2Ctx, "researcher") {
		t.Errorf("s2 ContextSummary should mention upstream agent type; got %q", s2Ctx)
	}
}

func TestExecutor_ParallelExecution(t *testing.T) {
	// Two independent steps should run concurrently. We synchronize with a
	// barrier: both handlers wait on the barrier; if scheduling were strictly
	// sequential the test would deadlock past its timeout.
	sp := newRecordingSpawner()
	var barrier sync.WaitGroup
	barrier.Add(2)
	sp.on("a", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		barrier.Done()
		barrier.Wait()
		return successResult("a", "A done"), nil
	})
	sp.on("b", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		barrier.Done()
		barrier.Wait()
		return successResult("b", "B done"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "a", SubagentType: "researcher", Task: "查 A"},
		{StepID: "b", SubagentType: "researcher", Task: "查 B"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := exec.Execute(ctx, plan, ExecuteOptions{})

	if res.Status != StatusCompleted {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestExecutor_FailureCascadesToSkipped(t *testing.T) {
	sp := newRecordingSpawner()
	// s1 always fails (3 attempts).
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return nil, errors.New("boom")
	})
	var s2Called int32
	sp.on("s2", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		atomic.AddInt32(&s2Called, 1)
		return successResult("s2", "should not run"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "researcher", Task: "查"},
		{StepID: "s2", SubagentType: "writer", Task: "写", DependsOn: []string{"s1"}},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusFailed {
		t.Errorf("status = %s, want %s", res.Status, StatusFailed)
	}
	if res.Steps[0].Status != StepStatusFailed {
		t.Errorf("s1 status = %s", res.Steps[0].Status)
	}
	if res.Steps[1].Status != StepStatusSkipped {
		t.Errorf("s2 should be skipped, got %s", res.Steps[1].Status)
	}
	if atomic.LoadInt32(&s2Called) != 0 {
		t.Errorf("s2 spawner should not have been invoked")
	}
	// s1 should be retried up to MaxStepRetries+1 times.
	if got := res.Steps[0].Attempts; got != MaxStepRetries+1 {
		t.Errorf("s1 attempts = %d, want %d", got, MaxStepRetries+1)
	}
}

func TestExecutor_PartialCompleted(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("ok", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("ok", "good"), nil
	})
	sp.on("bad", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return nil, errors.New("nope")
	})

	plan := &Plan{Steps: []Step{
		{StepID: "ok", SubagentType: "worker", Task: "do good"},
		{StepID: "bad", SubagentType: "worker", Task: "do bad"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusPartialCompleted {
		t.Errorf("status = %s, want %s", res.Status, StatusPartialCompleted)
	}
}

func TestExecutor_StepRetrySucceedsOnSecondAttempt(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("flaky", func(call int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		if call == 1 {
			return nil, errors.New("transient")
		}
		return successResult("flaky", "second-time-lucky"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "flaky", SubagentType: "worker", Task: "do thing"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusCompleted {
		t.Errorf("status = %s", res.Status)
	}
	if res.Steps[0].Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Steps[0].Attempts)
	}
}

func TestExecutor_DeliverableAggregation(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s1", "summary",
			types.Deliverable{FilePath: "/tmp/a.md"}), nil
	})
	sp.on("s2", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s2", "summary",
			types.Deliverable{FilePath: "/tmp/b.md"}), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "writer", Task: "write A"},
		{StepID: "s2", SubagentType: "writer", Task: "write B"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if got := len(res.Deliverables); got != 2 {
		t.Errorf("aggregated deliverables = %d, want 2", got)
	}
}

func TestExecutor_DeepCascade(t *testing.T) {
	// chain a → b → c → d, with `a` failing. b, c, d should all be skipped.
	sp := newRecordingSpawner()
	sp.on("a", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return nil, errors.New("fail at root")
	})
	for _, n := range []string{"b", "c", "d"} {
		n := n
		sp.on(n, func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
			return nil, fmt.Errorf("%s should not have run", n)
		})
	}
	plan := &Plan{Steps: []Step{
		{StepID: "a", SubagentType: "worker", Task: "x"},
		{StepID: "b", SubagentType: "worker", Task: "y", DependsOn: []string{"a"}},
		{StepID: "c", SubagentType: "worker", Task: "z", DependsOn: []string{"b"}},
		{StepID: "d", SubagentType: "worker", Task: "w", DependsOn: []string{"c"}},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{})

	if res.Status != StatusFailed {
		t.Errorf("status = %s", res.Status)
	}
	for _, step := range res.Steps[1:] {
		if step.Status != StepStatusSkipped {
			t.Errorf("%s status = %s, want skipped", step.StepID, step.Status)
		}
	}
}

func TestExecutor_MaxParallelBounded(t *testing.T) {
	// MaxParallel=1 forces sequential execution; verify it works without
	// deadlock and still completes.
	sp := newRecordingSpawner()
	plan := &Plan{Steps: []Step{
		{StepID: "a", SubagentType: "worker", Task: "1"},
		{StepID: "b", SubagentType: "worker", Task: "2"},
		{StepID: "c", SubagentType: "worker", Task: "3"},
	}}
	exec := NewPlanExecutor(sp, zap.NewNop())
	res := exec.Execute(context.Background(), plan, ExecuteOptions{MaxParallel: 1})

	if res.Status != StatusCompleted {
		t.Errorf("status = %s", res.Status)
	}
	if len(sp.calls) != 3 {
		t.Errorf("calls = %d", len(sp.calls))
	}
}
