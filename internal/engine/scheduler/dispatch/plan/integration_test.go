package plan_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/subagent"
	"harnessclaw-go/internal/workspace"
)

// planWritingFactory is a ContextFactory that, for planner tasks (goal prefix
// "Generate execution plan"), also writes plan.json alongside meta.json in the
// planner's taskDir. This simulates a real planner sub-agent producing a plan.
type planWritingFactory struct {
	rootDir string
	bus     msgbus.Bus
	staging tstate.StagingWriter
}

func (f *planWritingFactory) Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) subagent.LeafContext {
	taskDir := workspace.TaskDir(f.rootDir, sessionID, string(taskID))
	sessionRoot := workspace.SessionRoot(f.rootDir, sessionID)
	_ = os.MkdirAll(taskDir, 0o755)

	ws := &plannerWS{
		taskDir:     taskDir,
		sessionRoot: sessionRoot,
		isPlanner:   strings.HasPrefix(sp.Goal, "Generate execution plan"),
	}

	return subagent.LeafContext{
		TaskID:    taskID,
		SessionID: sessionID,
		SpecRef:   sp,
		Workspace: ws,
		Staging:   f.staging,
		Bus:       f.bus,
	}
}

// plannerWS is a WorkspaceHandle that, when WriteMeta is called on a planner
// task, also writes plan.json with one stub step.
type plannerWS struct {
	taskDir     string
	sessionRoot string
	isPlanner   bool
}

func (w *plannerWS) TaskDir() string { return w.taskDir }

func (w *plannerWS) MetaPath() string { return filepath.Join(w.taskDir, "meta.json") }

func (w *plannerWS) MetaRelPath() string {
	rel, err := filepath.Rel(w.sessionRoot, w.MetaPath())
	if err != nil {
		return "meta.json"
	}
	return rel
}

func (w *plannerWS) ReadScope() []string  { return []string{w.taskDir} }
func (w *plannerWS) WriteScope() []string { return []string{w.taskDir} }
func (w *plannerWS) InputPaths() []string { return nil }

func (w *plannerWS) WriteFile(_ context.Context, relPath string, data []byte) error {
	abs := filepath.Join(w.taskDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func (w *plannerWS) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.taskDir, relPath))
}

func (w *plannerWS) WriteMeta(ctx context.Context, m workspace.Meta) (string, error) {
	// If this is the planner task, also write plan.json with one step.
	if w.isPlanner {
		type planJSON struct {
			Steps []spec.TaskSpec `json:"steps"`
		}
		plan := planJSON{
			Steps: []spec.TaskSpec{
				{
					Goal:   "step 1",
					Hint:   spec.Hint{Kind: types.KindLeaf},
					Layout: "flat",
				},
			},
		}
		b, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return "", err
		}
		planPath := filepath.Join(w.taskDir, "plan.json")
		if err := os.WriteFile(planPath, b, 0o644); err != nil {
			return "", err
		}
	}
	return subagent.WriteMeta(ctx, w.taskDir, w.sessionRoot, m)
}

// TestPlanStrategy_E2E_WithRealFactory verifies the full plan execution path:
//  1. Parent plan task is submitted.
//  2. Planner leaf runs and writes plan.json with one step.
//  3. strategy.go reads plan.json, spawns the step child.
//  4. Step child completes; parent is woken.
//  5. Summarizer leaf runs.
//  6. Parent plan task reaches StatusSucceeded.
func TestPlanStrategy_E2E_WithRealFactory(t *testing.T) {
	tempRoot := t.TempDir()
	sessionID := "sess-plan-e2e-1"

	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)

	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("pe-")})
	staging := tstate.NewStagingWriter(tst)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sched := scheduler.New(scheduler.Config{
		Logger:    logger,
		Bus:       bus,
		Kernel:    kernel,
		Staging:   staging,
		ReactCaps: dispatch.Capabilities{LeafKind: "react-leaf"},
		PlanCaps: dispatch.Capabilities{
			AllowSubmit: true,
			RootDir:     tempRoot,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = bus.Close(); _ = tst.Close() }()

	sched.Start(ctx)

	factory := &planWritingFactory{rootDir: tempRoot, bus: bus, staging: staging}
	pool := subagent.NewConsumerPool(bus, kernel, factory, 4)
	pool.Start(ctx)

	testCtx, testCancel := context.WithTimeout(ctx, 15*time.Second)
	defer testCancel()

	taskID, err := sched.Submit(testCtx, spec.TaskSpec{
		Goal:      "build a feature",
		Hint:      spec.Hint{Kind: types.KindPlan},
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if taskID == "" {
		t.Fatal("Submit returned empty taskID")
	}

	// Poll for the parent task to reach StatusSucceeded.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ts, getErr := kernel.Get(testCtx, taskID)
		if getErr != nil {
			t.Fatalf("kernel.Get: %v", getErr)
		}
		if ts.Status == types.StatusSucceeded {
			// Verify ResultRef is set.
			if ts.ResultRef == "" {
				t.Error("expected ResultRef to be populated on succeeded plan task")
			}
			// Verify the plan task had children (the step).
			children, err := kernel.ListChildren(testCtx, taskID)
			if err != nil {
				t.Fatalf("ListChildren: %v", err)
			}
			// Expect at least 3 children: planner + step + summarizer.
			if len(children) < 3 {
				t.Errorf("expected at least 3 children (planner + step + summarizer), got %d", len(children))
			}
			return
		}
		if ts.Status.IsTerminal() && ts.Status != types.StatusSucceeded {
			t.Fatalf("plan task reached terminal %q, wanted %q (last_error=%q)", ts.Status, types.StatusSucceeded, ts.LastError)
		}
		time.Sleep(20 * time.Millisecond)
	}

	ts, _ := kernel.Get(testCtx, taskID)
	t.Fatalf("timed out after 10s waiting for plan task to succeed; current status=%s last_error=%q", ts.Status, ts.LastError)
}
