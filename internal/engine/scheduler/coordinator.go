package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/router"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/subagent"
	"harnessclaw-go/internal/workspace"
	pkgtypes "harnessclaw-go/pkg/types"
)

// CoordinatorConfig holds dependencies for NewCoordinator.
type CoordinatorConfig struct {
	// Spawner is the real (or fake) AgentSpawner used by QueryEngineFactory.
	Spawner agent.AgentSpawner

	// RootDir is the workspace root directory for all task outputs.
	RootDir string

	// Logger is optional; defaults to stderr at WarnLevel.
	Logger *slog.Logger

	// Provider is the LLM provider used by LLMKindSelector for task classification.
	// When non-nil, LLMKindSelector is used instead of HeuristicKindSelector
	// (unless KindSelector is also set, which takes precedence).
	Provider provider.Provider

	// KindSelector decides which execution kind (react/plan) to use for a task.
	// nil + Provider set → LLMKindSelector. nil + no Provider → HeuristicKindSelector.
	KindSelector router.KindSelector

	// AgentResolver picks which named agent executes a task step.
	// nil → HeuristicAgentResolver.
	AgentResolver router.AgentResolver
}

// Coordinator wires the L2 scheduler, ConsumerPool, and
// QueryEngineFactory together into a single callable object.
// Create with NewCoordinator; call Start before Run.
type Coordinator struct {
	cfg     CoordinatorConfig
	sched   *Scheduler
	pool    *subagent.ConsumerPool
	factory *subagent.QueryEngineFactory
	bus     msgbus.Bus
	kernel  tstate.Kernel
	staging tstate.StagingWriter
	kindSel router.KindSelector
}

// NewCoordinator creates a Coordinator backed by in-memory
// bus and tstate stores. The caller must invoke Start(ctx) before Run.
func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	// --- msgbus ---
	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)

	// --- tstate ---
	// Root coordinator task gets "t-0" (internal, no workspace dir).
	// First visible L3 leaf task starts at "t-1".
	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDsFrom("t-", 0)})
	staging := tstate.NewStagingWriter(tst)

	// --- L2 scheduler ---
	sched := New(Config{
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

	// --- KindSelector / AgentResolver defaults ---
	kindSel := cfg.KindSelector
	if kindSel == nil && cfg.Provider != nil {
		kindSel = router.NewLLMKindSelector(cfg.Provider)
	}
	if kindSel == nil {
		kindSel = router.NewHeuristicKindSelector()
	}
	agentRes := cfg.AgentResolver
	if agentRes == nil {
		agentRes = router.NewHeuristicAgentResolver()
	}

	// --- ConsumerPool via QueryEngineFactory ---
	factory := subagent.NewQueryEngineFactory(cfg.Spawner, cfg.RootDir, "").
		WithStagingAndBus(staging, bus).
		WithAgentResolver(agentRes)
	pool := subagent.NewConsumerPool(bus, kernel, factory, 4)

	return &Coordinator{
		cfg:     cfg,
		sched:   sched,
		pool:    pool,
		factory: factory,
		bus:     bus,
		kernel:  kernel,
		staging: staging,
		kindSel: kindSel,
	}
}

// CoordinatorTaskSpec builds a minimal TaskSpec for a coordinator-tier spawn.
func CoordinatorTaskSpec(goal string) spec.TaskSpec {
	return spec.TaskSpec{Goal: goal, Layout: "flat"}
}

// Start launches the scheduler and consumer pool goroutines.
// ctx cancellation stops all background work.
func (sc *Coordinator) Start(ctx context.Context) {
	sc.sched.Start(ctx)
	sc.pool.Start(ctx)
}

// Run submits a TaskSpec to the L2 scheduler and blocks until the task
// reaches a terminal state (succeeded or failed).
// Returns the MetaRef stored in tstate on success.
//
// outCh is optional. When non-nil, L3 lifecycle events (start/complete/fail)
// are forwarded to it so the caller can surface progress to the client. The
// channel must be writable and non-blocking writes are used (events are dropped
// if the channel is full rather than blocking the scheduler).
//
// Implementation note: this uses tstate polling rather than bus subscriptions
// to avoid a startup race. The scheduler router goroutine (started by
// sched.Start) may not have subscribed to the bus yet when the first lifecycle
// message is published, causing the message to be lost. Polling tstate directly
// is immune to that race and only requires the kernel, which is always ready.
func (sc *Coordinator) Run(ctx context.Context, sp spec.TaskSpec, outCh chan<- pkgtypes.EngineEvent) (types.MetaRef, error) {
	if sp.Hint.Kind == "" && sc.kindSel != nil {
		sp.Hint.Kind = sc.kindSel.Select(sp.Goal)
	}
	if sp.Hint.Kind == "" {
		sp.Hint.Kind = types.KindReact
	}

	// Wire outCh into the factory so every L3 SpawnSync call will forward
	// its events (SubAgentStart/End, tool calls, intents) directly to the
	// client instead of being silently discarded.
	if outCh != nil {
		sc.factory.SetOutCh(outCh)
		defer sc.factory.SetOutCh(nil)
	}

	taskID, err := sc.sched.Submit(ctx, sp)
	if err != nil {
		return "", fmt.Errorf("scheduler_coordinator: submit: %w", err)
	}

	// Poll tstate until the task reaches a terminal status.
	// 5 ms is fast enough for tests and low enough overhead for production use.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("scheduler_coordinator: context cancelled waiting for task %s: %w", taskID, ctx.Err())
		case <-ticker.C:
			ts, err := sc.kernel.Get(ctx, taskID)
			if err != nil {
				return "", fmt.Errorf("scheduler_coordinator: get task %s: %w", taskID, err)
			}
			switch ts.Status {
			case types.StatusSucceeded:
				ref := types.MetaRef(ts.ResultRef)
				sc.promoteToDeliverables(sp.SessionID, ref)
				return ref, nil
			case types.StatusFailed, types.StatusCancelled:
				return "", fmt.Errorf("scheduler_coordinator: task %s terminal: %s (last_error=%q)", taskID, ts.Status, ts.LastError)
			}
		}
	}
}

// promoteToDeliverables copies every output file declared in the final task's
// meta.json into {sessionRoot}/deliverables/ so L1 (emma) has a single,
// stable directory to point the user at. Errors are best-effort: a missing
// source file or a write failure is logged but never propagates to the caller.
func (sc *Coordinator) promoteToDeliverables(sessionID string, ref types.MetaRef) {
	if sc.cfg.RootDir == "" || sessionID == "" || ref == "" {
		return
	}
	sessionRoot := workspace.SessionRoot(sc.cfg.RootDir, sessionID)
	absMetaPath := filepath.Join(sessionRoot, string(ref))
	b, err := os.ReadFile(absMetaPath)
	if err != nil {
		return
	}
	var m workspace.Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	delivDir := workspace.DeliverablesDir(sc.cfg.RootDir, sessionID)
	if err := os.MkdirAll(delivDir, 0o755); err != nil {
		return
	}
	for _, o := range m.Outputs {
		if o.Path == "" {
			continue
		}
		src := o.Path
		if !filepath.IsAbs(src) {
			src = filepath.Join(sessionRoot, o.Path)
		}
		dest := filepath.Join(delivDir, filepath.Base(src))
		if err := copyFile(src, dest); err != nil {
			sc.cfg.Logger.Warn("promote: copy failed", slog.String("src", src), slog.String("err", err.Error()))
		}
	}
}

// copyFile copies src to dst atomically via a temp file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
