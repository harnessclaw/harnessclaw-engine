package scheduler

import (
	"context"
	"log/slog"
	"os"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/worker"
	"harnessclaw-go/internal/engine/agent/scheduler/dispatch"
	"harnessclaw-go/internal/engine/agent/scheduler/router"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	tstore "harnessclaw-go/internal/engine/agent/scheduler/tstate/store"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	mstore "harnessclaw-go/internal/msgbus/store"
	"harnessclaw-go/internal/provider"
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
	pool    *worker.ConsumerPool
	factory *worker.Factory
	events  *worker.EventRegistry
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
	sched := NewScheduler(Config{
		Logger:  logger,
		Bus:     bus,
		Kernel:  kernel,
		Staging: staging,
		ReactCaps: dispatch.Capabilities{
			AllowedTools: []string{"Read"},
			LeafKind:     "react-leaf",
		},
		PlanCaps: dispatch.Capabilities{
			AllowSubmit: true,
			RootDir:     cfg.RootDir,
		},
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

	// --- ConsumerPool via worker.Factory ---
	events := worker.NewEventRegistry()
	factory := worker.NewFactory(cfg.Spawner, cfg.RootDir, "").
		WithStagingAndBus(staging, bus).
		WithAgentResolver(agentRes).
		WithEvents(events, kernel)
	pool := worker.NewConsumerPool(bus, kernel, factory, 4)

	return &Coordinator{
		cfg:     cfg,
		sched:   sched,
		pool:    pool,
		factory: factory,
		events:  events,
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

// Submit admits a TaskSpec into the L2 scheduler and returns the root
// task ID. The Coordinator no longer blocks on the task — the waiting
// loop and deliverables promotion now live inside agentrun.ModeScheduled
// (see internal/agentrun.runScheduled), which uses the accessors below.
func (sc *Coordinator) Submit(ctx context.Context, sp spec.TaskSpec) (types.TaskID, error) {
	return sc.sched.Submit(ctx, sp)
}

// Get returns the current TaskState for id. Wraps the kernel reader so
// agentrun can poll terminal status without importing tstate's Kernel
// implementation directly.
func (sc *Coordinator) Get(ctx context.Context, id types.TaskID) (tstate.TaskState, error) {
	return sc.kernel.Get(ctx, id)
}

// RegisterEvents binds a per-Run event channel to the root task ID so
// every L3 sub-agent triggered under that root forwards its lifecycle
// events back to the caller. Unregister releases the binding.
func (sc *Coordinator) RegisterEvents(id types.TaskID, ch chan<- pkgtypes.EngineEvent) {
	sc.events.Register(id, ch)
}

// UnregisterEvents releases a binding previously created via RegisterEvents.
func (sc *Coordinator) UnregisterEvents(id types.TaskID) {
	sc.events.Unregister(id)
}

// SelectKind classifies a goal into a scheduler Kind (react / plan) using
// the configured KindSelector. Returns empty Kind when no selector is
// configured; callers should fall back to KindReact in that case.
func (sc *Coordinator) SelectKind(goal string) types.Kind {
	if sc.kindSel == nil {
		return ""
	}
	return sc.kindSel.Select(goal)
}

// RootDir returns the workspace root the Coordinator was configured with.
// Used by agentrun.runScheduled to locate session directories during
// deliverables promotion.
func (sc *Coordinator) RootDir() string {
	return sc.cfg.RootDir
}

// Logger returns the structured logger the Coordinator was configured
// with. Used by agentrun.runScheduled to warn on best-effort promotion
// errors.
func (sc *Coordinator) Logger() *slog.Logger {
	return sc.cfg.Logger
}
