package plan_test

import (
	"context"
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
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/subagent"
	"harnessclaw-go/internal/workspace"
)

// planModeFactory simulates plan-agent + plan-executor-agent by writing directly
// to plan.json (plan-agent phase) and marking tasks done (plan-executor-agent phase).
type planModeFactory struct {
	rootDir  string
	bus      msgbus.Bus
	staging  tstate.StagingWriter
	registry *workspace.PlanWriterRegistry
}

func newPlanModeFactory(rootDir string, bus msgbus.Bus, staging tstate.StagingWriter) *planModeFactory {
	return &planModeFactory{
		rootDir:  rootDir,
		bus:      bus,
		staging:  staging,
		registry: workspace.NewPlanWriterRegistry(rootDir),
	}
}

func (f *planModeFactory) Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) subagent.LeafContext {
	taskDir := workspace.TaskDir(f.rootDir, sessionID, string(taskID))
	sessionRoot := workspace.SessionRoot(f.rootDir, sessionID)
	_ = os.MkdirAll(taskDir, 0o755)

	ws := &planModeWS{
		taskDir:      taskDir,
		sessionRoot:  sessionRoot,
		rootDir:      f.rootDir,
		sessionID:    sessionID,
		registry:     f.registry,
		subagentType: sp.SubagentType,
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

type planModeWS struct {
	taskDir      string
	sessionRoot  string
	rootDir      string
	sessionID    string
	registry     *workspace.PlanWriterRegistry
	subagentType string
}

func (w *planModeWS) TaskDir() string  { return w.taskDir }
func (w *planModeWS) MetaPath() string { return filepath.Join(w.taskDir, "meta.json") }
func (w *planModeWS) MetaRelPath() string {
	rel, err := filepath.Rel(w.sessionRoot, w.MetaPath())
	if err != nil {
		return "meta.json"
	}
	return rel
}
func (w *planModeWS) ReadScope() []string  { return []string{w.taskDir} }
func (w *planModeWS) WriteScope() []string { return []string{w.taskDir} }
func (w *planModeWS) InputPaths() []string { return nil }

func (w *planModeWS) WriteFile(_ context.Context, relPath string, data []byte) error {
	abs := filepath.Join(w.taskDir, relPath)
	_ = os.MkdirAll(filepath.Dir(abs), 0o755)
	return os.WriteFile(abs, data, 0o644)
}

func (w *planModeWS) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.taskDir, relPath))
}

func (w *planModeWS) WriteMeta(ctx context.Context, m workspace.Meta) (string, error) {
	switch {
	case strings.HasPrefix(w.subagentType, "plan-agent"):
		// Simulate plan-agent: write 1 stub task to plan.json via PlanWriterRegistry.
		pw := w.registry.Get(w.sessionID)
		_ = pw.Apply(ctx, func(p *workspace.Plan) error {
			if p.Tasks == nil {
				p.Tasks = map[string]*workspace.Task{}
			}
			p.Tasks["step-1"] = &workspace.Task{
				Title:  "stub step from plan-agent",
				Agent:  "freelancer",
				Status: workspace.StatusPending,
			}
			return nil
		})

	case strings.HasPrefix(w.subagentType, "plan-executor-agent"):
		// Simulate plan-executor-agent: mark all pending/running tasks done.
		pw := w.registry.Get(w.sessionID)
		_ = pw.Apply(ctx, func(p *workspace.Plan) error {
			for id, task := range p.Tasks {
				if task.Status == workspace.StatusPending || task.Status == workspace.StatusRunning {
					p.Tasks[id].Status = workspace.StatusDone
					p.Tasks[id].SummaryRef = "tasks/" + id + "/meta.json"
				}
			}
			return nil
		})
	}

	return subagent.WriteMeta(ctx, w.taskDir, w.sessionRoot, m)
}

// TestPlanStrategy_E2E_TwoAgentPhases verifies the new plan execution path:
//  1. plan-agent task runs and writes task breakdown to plan.json.
//  2. plan-executor-agent task runs and marks all tasks done.
//  3. Parent plan task reaches StatusSucceeded.
func TestPlanStrategy_E2E_TwoAgentPhases(t *testing.T) {
	tempRoot := t.TempDir()
	sessionID := "sess-plan-e2e-v2"

	if err := workspace.EnsureSession(tempRoot, sessionID); err != nil {
		t.Fatal(err)
	}

	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)
	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("pe2-")})
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

	factory := newPlanModeFactory(tempRoot, bus, staging)
	defer factory.registry.StopAll(context.Background())
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

	// Poll until the parent plan task reaches StatusSucceeded.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ts, getErr := kernel.Get(testCtx, taskID)
		if getErr != nil {
			t.Fatalf("kernel.Get: %v", getErr)
		}
		if ts.Status == types.StatusSucceeded {
			if ts.ResultRef == "" {
				t.Error("expected ResultRef to be populated on succeeded plan task")
			}
			// Expect exactly 2 children: plan-agent + plan-executor-agent.
			children, err := kernel.ListChildren(testCtx, taskID)
			if err != nil {
				t.Fatalf("ListChildren: %v", err)
			}
			if len(children) != 2 {
				t.Errorf("expected exactly 2 children (plan-agent + plan-executor-agent), got %d", len(children))
			}
			return
		}
		if ts.Status.IsTerminal() && ts.Status != types.StatusSucceeded {
			t.Fatalf("plan task reached terminal %q (last_error=%q)", ts.Status, ts.LastError)
		}
		time.Sleep(20 * time.Millisecond)
	}

	ts, _ := kernel.Get(testCtx, taskID)
	t.Fatalf("timed out; status=%s last_error=%q", ts.Status, ts.LastError)
}
