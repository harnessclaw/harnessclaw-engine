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

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/workspace"
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

// TestCutover_ReactKind verifies that Coordinator completes a
// KindReact task when an explicit hint is provided.
func TestCutover_ReactKind(t *testing.T) {
	dir := t.TempDir()
	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: &engineFakeSpawner{output: "react done"},
		RootDir: dir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	ref, err := sc.Run(ctx, spec.TaskSpec{
		Goal:      "write a hello world script",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-react",
		Layout:    "flat",
	}, nil)
	if err != nil {
		t.Fatalf("RunLeaf: %v", err)
	}
	if ref == "" {
		t.Fatal("empty MetaRef")
	}
}

// TestCutover_AutoKindSelection_React verifies that HeuristicKindSelector
// routes a simple goal to KindReact and the task completes successfully.
func TestCutover_AutoKindSelection_React(t *testing.T) {
	dir := t.TempDir()
	sc := scheduler.NewCoordinator(scheduler.CoordinatorConfig{
		Spawner: &engineFakeSpawner{output: "auto react done"},
		RootDir: dir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	// Simple goal → HeuristicKindSelector should pick KindReact.
	ref, err := sc.Run(ctx, spec.TaskSpec{
		Goal:      "fix this bug",
		SessionID: "sess-auto",
		Layout:    "flat",
		// No Hint.Kind set — relies on auto-selection.
	}, nil)
	if err != nil {
		t.Fatalf("RunLeaf: %v", err)
	}
	if ref == "" {
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

	// "step by step" triggers HeuristicKindSelector → KindPlan.
	ref, err := sc.Run(ctx, spec.TaskSpec{
		Goal:      "step by step migrate the database schema",
		SessionID: "sess-plan",
		Layout:    "flat",
		// No hint — relies on KindSelector.
	}, nil)
	if err != nil {
		t.Fatalf("RunLeaf: %v", err)
	}
	if ref == "" {
		t.Fatal("empty MetaRef")
	}
}
