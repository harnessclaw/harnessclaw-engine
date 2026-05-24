package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"harnessclaw-go/internal/agent"
	l2sched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/router"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/subagent"
)

// SchedulerCoordinatorConfig holds dependencies for NewSchedulerCoordinator.
type SchedulerCoordinatorConfig struct {
	// Spawner is the real (or fake) AgentSpawner used by QueryEngineFactory.
	Spawner agent.AgentSpawner

	// RootDir is the workspace root directory for all task outputs.
	RootDir string

	// Logger is optional; defaults to stderr at WarnLevel.
	Logger *slog.Logger

	// KindSelector decides which execution kind (react/plan) to use for a task.
	// nil → HeuristicKindSelector.
	KindSelector router.KindSelector

	// AgentResolver picks which named agent executes a task step.
	// nil → HeuristicAgentResolver.
	AgentResolver router.AgentResolver
}

// SchedulerCoordinator wires the L2 scheduler, ConsumerPool, and
// QueryEngineFactory together into a single callable object.
// Create with NewSchedulerCoordinator; call Start before RunLeaf.
type SchedulerCoordinator struct {
	cfg     SchedulerCoordinatorConfig
	sched   *l2sched.Scheduler
	pool    *subagent.ConsumerPool
	bus     msgbus.Bus
	kernel  tstate.Kernel
	staging tstate.StagingWriter
	kindSel router.KindSelector
}

// NewSchedulerCoordinator creates a SchedulerCoordinator backed by in-memory
// bus and tstate stores. The caller must invoke Start(ctx) before RunLeaf.
func NewSchedulerCoordinator(cfg SchedulerCoordinatorConfig) *SchedulerCoordinator {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	// --- msgbus ---
	mst := mstore.NewMemory()
	bus := msgbus.NewInMem(mst)

	// --- tstate ---
	tst := tstore.NewMemory()
	kernel := tstate.NewKernel(tst, tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	staging := tstate.NewStagingWriter(tst)

	// --- L2 scheduler ---
	sched := l2sched.New(l2sched.Config{
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
	if kindSel == nil {
		kindSel = router.NewHeuristicKindSelector()
	}
	agentRes := cfg.AgentResolver
	if agentRes == nil {
		agentRes = router.NewHeuristicAgentResolver()
	}

	// --- ConsumerPool via QueryEngineFactory ---
	factory := subagent.NewQueryEngineFactory(cfg.Spawner, cfg.RootDir, "root").
		WithStagingAndBus(staging, bus).
		WithAgentResolver(agentRes)
	pool := subagent.NewConsumerPool(bus, kernel, factory, 4)

	return &SchedulerCoordinator{
		cfg:     cfg,
		sched:   sched,
		pool:    pool,
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
func (sc *SchedulerCoordinator) Start(ctx context.Context) {
	sc.sched.Start(ctx)
	sc.pool.Start(ctx)
}

// RunLeaf submits a TaskSpec to the L2 scheduler and blocks until the task
// reaches a terminal state (succeeded or failed).
// Returns the MetaRef stored in tstate on success.
//
// Implementation note: this uses tstate polling rather than bus subscriptions
// to avoid a startup race. The scheduler router goroutine (started by
// sched.Start) may not have subscribed to the bus yet when the first lifecycle
// message is published, causing the message to be lost. Polling tstate directly
// is immune to that race and only requires the kernel, which is always ready.
func (sc *SchedulerCoordinator) RunLeaf(ctx context.Context, _ string, sp spec.TaskSpec) (types.MetaRef, error) {
	if sp.Hint.Kind == "" && sc.kindSel != nil {
		sp.Hint.Kind = sc.kindSel.Select(sp.Goal)
	}
	if sp.Hint.Kind == "" {
		sp.Hint.Kind = types.KindReact
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
				return types.MetaRef(ts.ResultRef), nil
			case types.StatusFailed, types.StatusCancelled:
				return "", fmt.Errorf("scheduler_coordinator: task %s terminal: %s (last_error=%q)", taskID, ts.Status, ts.LastError)
			}
		}
	}
}
