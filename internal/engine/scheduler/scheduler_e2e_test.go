// Package scheduler_test contains end-to-end tests for the L2 scheduler.
// They wire together in-memory msgbus + tstate + ConsumerPool + strategies
// and assert the full Submit→lifecycle→result→succeed chain.
package scheduler_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// ─── shared helpers ───────────────────────────────────────────────────────────

// stubFactory builds a LeafContext whose WorkspaceHandle writes to a temp dir.
type stubFactory struct {
	rootDir string
	bus     msgbus.Bus
	staging tstate.StagingWriter
}

func (f *stubFactory) Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) subagent.LeafContext {
	sid := sessionID
	if sid == "" {
		sid = string(taskID)
	}
	wsRoot := workspace.SessionRoot(f.rootDir, sid)
	_ = os.MkdirAll(wsRoot, 0o755)
	return subagent.LeafContext{
		TaskID:    taskID,
		SessionID: sid,
		SpecRef:   sp,
		Workspace: &stubWS{root: wsRoot},
		Staging:   f.staging,
		Bus:       f.bus,
	}
}

type stubWS struct{ root string }

func (s *stubWS) TaskDir() string     { return s.root }
func (s *stubWS) MetaPath() string    { return filepath.Join(s.root, "meta.json") }
func (s *stubWS) MetaRelPath() string { return "meta.json" }
func (s *stubWS) ReadScope() []string { return []string{s.root} }
func (s *stubWS) WriteScope() []string { return []string{s.root} }
func (s *stubWS) InputPaths() []string { return nil }

func (s *stubWS) WriteFile(_ context.Context, rel string, b []byte) error {
	return os.WriteFile(filepath.Join(s.root, rel), b, 0o644)
}

func (s *stubWS) ReadFile(_ context.Context, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, rel))
}

func (s *stubWS) WriteMeta(ctx context.Context, m workspace.Meta) (string, error) {
	return subagent.WriteMeta(ctx, s.root, s.root, m)
}

// newTestEnv returns a wired scheduler + kernel + bus backed by in-memory stores.
// The test must cancel ctx to stop background goroutines.
func newTestEnv(t *testing.T) (*scheduler.Scheduler, tstate.Kernel, msgbus.Bus, *subagent.ConsumerPool, func()) {
	t.Helper()
	tempRoot := t.TempDir()

	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)

	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	staging := tstate.NewStagingWriter(tst)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sched := scheduler.New(scheduler.Config{
		Logger:  logger,
		Bus:     bus,
		Kernel:  kernel,
		Staging: staging,
		ReactCaps: dispatch.Capabilities{
			AllowedTools: []string{"Read"},
			LeafKind:     "react-leaf",
		},
		PlanCaps: dispatch.Capabilities{AllowSubmit: true},
	})

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	factory := &stubFactory{rootDir: tempRoot, bus: bus, staging: staging}
	pool := subagent.NewConsumerPool(bus, kernel, factory, 2)
	pool.Start(ctx)

	cleanup := func() {
		cancel()
		_ = bus.Close()
		_ = tst.Close()
	}
	return sched, kernel, bus, pool, cleanup
}

// waitForStatus polls kernel.Get until the task reaches the expected status or timeout.
func waitForStatus(t *testing.T, ctx context.Context, k tstate.Kernel, id types.TaskID, want types.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ts, err := k.Get(ctx, id)
		if err != nil {
			t.Fatalf("waitForStatus: Get(%s): %v", id, err)
		}
		if ts.Status == want {
			return
		}
		if ts.Status.IsTerminal() && ts.Status != want {
			t.Fatalf("waitForStatus: task %s reached terminal %q, wanted %q (last_error=%q)", id, ts.Status, want, ts.LastError)
		}
		time.Sleep(20 * time.Millisecond)
	}
	ts, _ := k.Get(ctx, id)
	t.Fatalf("waitForStatus: timed out after %v waiting for %s on %s; current=%s last_error=%q",
		timeout, want, id, ts.Status, ts.LastError)
}

// ─── Task 44: react Submit → worker → lifecycle{completed} → succeeded ────────

func TestE2EReactFullChain(t *testing.T) {
	sched, kernel, _, _, cleanup := newTestEnv(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	taskID, err := sched.Submit(ctx, spec.TaskSpec{
		Goal:      "read README",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-react-1",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if taskID == "" {
		t.Fatal("Submit returned empty taskID")
	}

	// The ConsumerPool processes the leaf child, publishes lifecycle{completed},
	// and the scheduler handlers drive it to succeeded.
	waitForStatus(t, ctx, kernel, taskID, types.StatusSucceeded, 4*time.Second)

	// Verify ResultRef is set on the parent (react) task.
	ts, err := kernel.Get(ctx, taskID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ts.ResultRef == "" {
		t.Error("expected ResultRef to be populated on succeeded task")
	}
}

// ─── Task 45: plan skeleton (no children) → succeeded ────────────────────────

func TestE2EPlanSkeletonSucceeds(t *testing.T) {
	sched, kernel, _, _, cleanup := newTestEnv(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	taskID, err := sched.Submit(ctx, spec.TaskSpec{
		Goal:      "produce a plan",
		Hint:      spec.Hint{Kind: types.KindPlan},
		SessionID: "sess-plan-1",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Plan strategy also spawns a leaf (planner), and the skeleton runner completes it.
	waitForStatus(t, ctx, kernel, taskID, types.StatusSucceeded, 4*time.Second)
}

// ─── Task 46: Admit rejects empty Goal ───────────────────────────────────────

func TestE2ESubmitEmptyGoalRejected(t *testing.T) {
	sched, _, _, _, cleanup := newTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	// Empty Goal: kernel.Admit must return an error; Submit propagates it.
	_, err := sched.Submit(ctx, spec.TaskSpec{
		Goal: "",
		Hint: spec.Hint{Kind: types.KindReact},
	})
	if err == nil {
		t.Fatal("expected Submit to fail on empty Goal, but got nil error")
	}
}

// TestE2ESubmitUnknownKindRejected verifies Submit rejects unregistered kinds.
func TestE2ESubmitUnknownKindRejected(t *testing.T) {
	sched, _, _, _, cleanup := newTestEnv(t)
	defer cleanup()

	ctx := context.Background()

	_, err := sched.Submit(ctx, spec.TaskSpec{
		Goal: "do something",
		Hint: spec.Hint{Kind: types.Kind("nonexistent-kind")},
	})
	if err == nil {
		t.Fatal("expected Submit to fail on unknown kind, but got nil error")
	}
}

// ─── Task 47: cancel → cancelling → cancelled ────────────────────────────────

func TestE2ECancelChain(t *testing.T) {
	// Use a separate scheduler without a consumer pool so the leaf task stays
	// in "running" status long enough for us to cancel the parent.
	tempRoot := t.TempDir()

	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)
	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("c-")})
	staging := tstate.NewStagingWriter(tst)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	sched := scheduler.New(scheduler.Config{
		Logger:    logger,
		Bus:       bus,
		Kernel:    kernel,
		Staging:   staging,
		ReactCaps: dispatch.Capabilities{AllowedTools: []string{"Read"}, LeafKind: "react-leaf"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = bus.Close(); _ = tst.Close() }()

	sched.Start(ctx)

	// Slow factory: spawns a leaf context but the consumer pool is not started,
	// so the leaf child will never advance past "ready". We cancel the parent immediately.
	_ = tempRoot // factory not wired to pool here

	taskCtx, taskCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer taskCancel()

	taskID, err := sched.Submit(taskCtx, spec.TaskSpec{
		Goal:      "long running thing",
		Hint:      spec.Hint{Kind: types.KindReact},
		SessionID: "sess-cancel-1",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The react strategy is now waiting for spawn_granted→result from SpawnAndWaitOne.
	// Cancel the task before any consumer picks it up.
	time.Sleep(50 * time.Millisecond) // let strategy goroutine reach SpawnAndWaitOne
	if err := sched.Cancel(taskCtx, taskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// The reaper will scan cancelling tasks and fire cancelling_drained once all
	// children are terminal. For this test the leaf child was never claimed, so
	// it transitions to cancelling too and (having no children of its own) is
	// terminal immediately. We manually run the reaper to avoid time-sensitive polling.
	reaper := host_reaper_for_test(kernel, bus)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reaper.RunOnce(taskCtx)
		ts, err := kernel.Get(taskCtx, taskID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if ts.Status == types.StatusCancelled {
			return // success
		}
		time.Sleep(30 * time.Millisecond)
	}
	ts, _ := kernel.Get(taskCtx, taskID)
	t.Fatalf("expected cancelled, got %s", ts.Status)
}

// host_reaper_for_test is defined in e2e_cancel_helpers_test.go
// to avoid circular import — we use a local reaper stub here.
func host_reaper_for_test(k tstate.Kernel, b msgbus.Bus) reaperIface {
	return &localReaper{k: k, b: b}
}

type reaperIface interface {
	RunOnce(ctx context.Context)
}

// localReaper mirrors the real Reaper's scan without importing the host package.
type localReaper struct {
	k tstate.Kernel
	b msgbus.Bus
}

func (r *localReaper) RunOnce(ctx context.Context) {
	// Scan cancelling tasks; if all children are terminal, publish cancelling_drained.
	tasks, err := r.k.ListByStatus(ctx, "", types.StatusCancelling, 0)
	if err != nil {
		return
	}
	for _, ts := range tasks {
		children, err := r.k.ListChildren(ctx, ts.ID)
		if err != nil {
			continue
		}
		drained := true
		for _, c := range children {
			if !c.Status.IsTerminal() {
				// Also cancel any non-terminal children.
				if c.Status != types.StatusCancelling {
					_ = r.k.Cancel(ctx, c.ID)
				}
				drained = false
			}
		}
		if drained {
			_ = r.b.Publish(ctx, msgbus.AgentMessage{
				MsgID:   fmt.Sprintf("reaper:drained:%s", ts.ID),
				Kind:    msgbus.KindNotify,
				From:    msgbus.AddrReaper,
				To:      msgbus.AddrScheduler,
				TaskID:  string(ts.ID),
				Payload: msgbus.NotifyPayload{Event: msgbus.NotifyCancellingDrained, TaskID: string(ts.ID)},
			})
		}
	}
}
