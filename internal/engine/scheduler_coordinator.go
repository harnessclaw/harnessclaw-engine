package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"harnessclaw-go/internal/agent"
	l2sched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/dispatch"
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

	// --- ConsumerPool via QueryEngineFactory ---
	factory := subagent.NewQueryEngineFactory(cfg.Spawner, cfg.RootDir, "root").
		WithStagingAndBus(staging, bus)
	pool := subagent.NewConsumerPool(bus, kernel, factory, 4)

	return &SchedulerCoordinator{
		cfg:     cfg,
		sched:   sched,
		pool:    pool,
		bus:     bus,
		kernel:  kernel,
		staging: staging,
	}
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
func (sc *SchedulerCoordinator) RunLeaf(ctx context.Context, _ string, sp spec.TaskSpec) (types.MetaRef, error) {
	// Subscribe BEFORE Submit to avoid the race documented in msgbus.SubscribeOnce.
	// We use a broad KindNotify filter first; once we have the taskID we can narrow,
	// but since we only have one task in-flight here, any NotifySucceeded matches.

	// Admit first to get the taskID, then set up a targeted subscription.
	// Use a buffered regular Subscribe on AddrScheduler to catch the event.
	ch, cancelSub := sc.bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSub()

	taskID, err := sc.sched.Submit(ctx, sp)
	if err != nil {
		return "", fmt.Errorf("scheduler_coordinator: submit: %w", err)
	}

	// Wait for NotifySucceeded or NotifyFailed for our taskID.
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("scheduler_coordinator: context cancelled waiting for task %s: %w", taskID, ctx.Err())
		case msg, ok := <-ch:
			if !ok {
				return "", fmt.Errorf("scheduler_coordinator: subscription closed for task %s", taskID)
			}
			if msg.Kind != msgbus.KindNotify || msg.TaskID != string(taskID) {
				continue
			}
			np, ok := msg.Payload.(msgbus.NotifyPayload)
			if !ok {
				continue
			}
			switch np.Event {
			case msgbus.NotifySucceeded:
				// Read the ResultRef from tstate.
				ts, err := sc.kernel.Get(ctx, taskID)
				if err != nil {
					return "", fmt.Errorf("scheduler_coordinator: get task %s after succeeded: %w", taskID, err)
				}
				return types.MetaRef(ts.ResultRef), nil
			case msgbus.NotifyFailed, msgbus.NotifyCancelled:
				ts, _ := sc.kernel.Get(ctx, taskID)
				return "", fmt.Errorf("scheduler_coordinator: task %s terminal: %s (last_error=%q)", taskID, np.Event, ts.LastError)
			}
		}
	}
}
