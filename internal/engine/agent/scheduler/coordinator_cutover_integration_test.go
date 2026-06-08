package scheduler_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/engine/agent/scheduler"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/legacy/workspace"
)

// planWritingFakeSpawner writes plan.json on the first call (planner) and
// succeeds silently on subsequent calls (step executors, summariser).
type planWritingFakeSpawner struct {
	rootDir string
	callNum int64 // accessed atomically
}

func (s *planWritingFakeSpawner) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	n := atomic.AddInt64(&s.callNum, 1)
	if n == 1 {
		// First call = planner. Write plan.json with one step.
		taskDir := workspace.TaskDir(s.rootDir, cfg.ParentSessionID, cfg.TaskID)
		_ = os.MkdirAll(taskDir, 0o755)

		type planStep struct {
			Goal   string `json:"goal"`
			Layout string `json:"layout"`
		}
		type planJSON struct {
			Steps []planStep `json:"steps"`
		}
		b, _ := json.Marshal(planJSON{Steps: []planStep{{Goal: "step 1", Layout: "flat"}}})
		_ = os.WriteFile(filepath.Join(taskDir, "plan.json"), b, 0o644)
	}
	return &agent.SpawnResult{Output: "spawner call " + strconv.FormatInt(n, 10)}, nil
}

// TestCutover_ReactKind verifies that agentrun.ModeScheduled completes
// a KindReact task when an explicit hint is provided.
func TestCutover_ReactKind(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "react done"}
	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)
	sp := spec.TaskSpec{
		Goal:      "write a hello world script",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-react",
		Layout:    "flat",
	}
	res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MetaRef == "" {
		t.Fatal("empty MetaRef")
	}
}

// TestCutover_AutoKindSelection_React verifies that HeuristicKindSelector
// routes a simple goal to KindReact and the task completes successfully.
func TestCutover_AutoKindSelection_React(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "auto react done"}
	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)
	// Simple goal → HeuristicKindSelector should pick KindReact.
	sp := spec.TaskSpec{
		Goal:      "fix this bug",
		SessionID: "sess-auto",
		Layout:    "flat",
		// No Hint.Kind set — relies on auto-selection.
	}
	res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MetaRef == "" {
		t.Fatal("empty MetaRef")
	}
}

// TestCutover_KindSelectorPlanGoal verifies that a multi-step goal is routed
// to KindPlan by HeuristicKindSelector and that the plan pipeline completes
// when the fake spawner writes a valid plan.json on the first (planner) call.
func TestCutover_KindSelectorPlanGoal(t *testing.T) {
	dir := t.TempDir()
	spawner := &planWritingFakeSpawner{rootDir: dir}
	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sc.Start(ctx)

	rt := agentrun.New(spawner).WithScheduler(sc)
	// "step by step" triggers HeuristicKindSelector → KindPlan.
	sp := spec.TaskSpec{
		Goal:      "step by step migrate the database schema",
		SessionID: "sess-plan",
		Layout:    "flat",
		// No hint — relies on KindSelector.
	}
	res, err := rt.Run(ctx, agentrun.Request{Spec: &sp, Mode: agentrun.ModeScheduled})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MetaRef == "" {
		t.Fatal("empty MetaRef")
	}
}
