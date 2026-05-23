package engine

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// CoordinatorMode names a way an L2 coordinator (e.g. scheduler) chooses
// to organize work. Modes are orthogonal to L3 dispatch strategies — the
// strategy decides how a single step fans out into sub-agents, the mode
// decides whether the coordinator runs as a free-form ReAct loop or as a
// pre-planned DAG (and, in the future, debate / hierarchical / etc.).
//
// Empty string ("") means "no preference" — the registry resolves it to
// the default mode for the agent (currently ReAct for every coordinator).
type CoordinatorMode string

const (
	// CoordinatorModeReAct is the historical scheduler behaviour: a
	// dispatch-capable LLM loop that thinks, calls Task to spawn L3
	// sub-agents, integrates results, and returns. Cheap, fast, suitable
	// for one-shot or short multi-step tasks.
	CoordinatorModeReAct CoordinatorMode = "react"

	// CoordinatorModePlan plans a step DAG up-front (Planner → Judge →
	// Scheduler), executes with budget tracking, judges per-step + per-goal,
	// and falls back gracefully on budget exhaustion. Heavyweight; suitable
	// for tasks with explicit multi-stage dependencies, fan-out, or branch
	// decisions.
	//
	// The Plan coordinator is currently a stub — see PlanCoordinator. Real
	// implementation will land in a follow-up; the interface and routing
	// are in place so we can add it without further surgery.
	CoordinatorModePlan CoordinatorMode = "plan"
)

// String makes CoordinatorMode usable in zap fields without conversions.
func (m CoordinatorMode) String() string { return string(m) }

// IsKnown returns true if m is a registered mode (not empty, not unknown).
func (m CoordinatorMode) IsKnown() bool {
	switch m {
	case CoordinatorModeReAct, CoordinatorModePlan:
		return true
	}
	return false
}

// Coordinator is the L2-side abstraction over "how does the scheduler organize
// its work?". Different modes (react / plan / debate / vote / ...) implement
// the same interface and are interchangeable at the SpawnSync routing point.
//
// Lifecycle: a coordinator is constructed per-spawn (cheap), Run is called
// once, and the coordinator is discarded. Coordinators are stateless beyond
// the SharedDeps they receive at construction.
type Coordinator interface {
	// Mode reports which CoordinatorMode this implementation realises.
	// Used for telemetry and escalation routing.
	Mode() CoordinatorMode

	// Run drives the coordinator to completion. The signature mirrors
	// runSubAgentLoop / runSubAgentDriver so existing wiring stays
	// compatible: the same loopConfig, session, and event channel are
	// passed through. Return value carries the loop's terminal state and
	// (where applicable) submitted artifacts / contract failures.
	Run(
		ctx context.Context,
		sess *session.Session,
		lc *loopConfig,
		out chan<- types.EngineEvent,
	) subAgentLoopResult
}

// SharedDeps bundles the cross-mode infrastructure every coordinator gets
// access to. ReAct and Plan see the same singletons so a budget started by
// the React run continues counting after promotion to Plan.
//
// SharedDeps is built per-spawn (cheap; just struct construction) and
// passed by pointer so escalation paths can mutate the BudgetTracker
// observed by both modes.
type SharedDeps struct {
	// QE is the parent query engine. Coordinators call back into engine
	// methods (runSubAgentLoop, helpers) through this handle. Holding a
	// pointer keeps the abstraction zero-cost and avoids re-plumbing every
	// field.
	QE *QueryEngine

	// Logger is the engine's named logger, available to coordinators
	// without re-importing zap configuration.
	Logger *zap.Logger

	// Budget is the per-task BudgetTracker. ReAct increments it as
	// runSubAgentLoop reports usage; Plan does the same for its
	// per-step LLM round-trips and Planner consultations. Shared so a
	// react→plan promotion doesn't reset the counter.
	Budget *BudgetTracker

	// Judge runs progressive rule → schema → LLM checks on plans /
	// step results / final goal output. Stateless, but built once per
	// task so the named logger ("judge") consistently appears in trace.
	Judge *Judge

	// Planner produces a Plan from a PlannerInput. Defaults to the
	// HeuristicPlanner; tests / advanced setups can swap in an LLM
	// planner via a custom factory.
	Planner Planner

	// Fallback aggregates partial step results into a graceful summary
	// when the coordinator can't deliver a fully-passing run. Both
	// modes use the same chain so degraded responses look identical
	// across modes.
	Fallback *FallbackChain

	// ModeSelector picks the CoordinatorMode at task entry (B-mode of
	// the B+D scheme). Defaults to HeuristicModeSelector; production
	// can swap in LLMModeSelector once the prompt is settled.
	ModeSelector ModeSelector

	// SubagentResolver decides which L3 sub-agent runs each plan step
	// at dispatch time. Replaces the v1.15- behaviour where Planner
	// pre-bound steps to executors. Defaults to HeuristicSubagentResolver.
	SubagentResolver SubagentResolver
}

// CoordinatorFactory builds a Coordinator instance for one spawn. Factories
// are registered in the global coordinatorRegistry keyed by CoordinatorMode.
type CoordinatorFactory func(deps *SharedDeps) Coordinator

// coordinatorRegistry is the mode → factory lookup. Concurrent reads are
// supported via RWMutex; registrations should happen during engine init,
// before any spawn fires.
type coordinatorRegistry struct {
	mu       sync.RWMutex
	factories map[CoordinatorMode]CoordinatorFactory
}

// newCoordinatorRegistry constructs an empty registry. Call Register before
// Resolve to install factories; built-in registrations happen in
// registerBuiltinCoordinators below.
func newCoordinatorRegistry() *coordinatorRegistry {
	return &coordinatorRegistry{factories: make(map[CoordinatorMode]CoordinatorFactory)}
}

// Register installs (or overwrites) a factory for mode. Returns the registry
// for chained calls during init.
func (r *coordinatorRegistry) Register(mode CoordinatorMode, factory CoordinatorFactory) *coordinatorRegistry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[mode] = factory
	return r
}

// Resolve picks the right factory. Resolution order:
//  1. preference is registered → use it
//  2. preference is empty / unknown → fall back to ReAct (the cheapest,
//     safest mode that all coordinator-tier agents support today)
//
// The fallback is intentional: a malformed mode parameter from the
// WebSocket should NOT crash the spawn; we log and continue with ReAct.
func (r *coordinatorRegistry) Resolve(preference CoordinatorMode, deps *SharedDeps) Coordinator {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if preference != "" {
		if factory, ok := r.factories[preference]; ok {
			return factory(deps)
		}
		// Unknown / not yet implemented mode — log once and fall through.
		// This is the path that fires when a client sends mode=plan
		// before PlanCoordinator is fully implemented.
		if deps.Logger != nil {
			deps.Logger.Warn("coordinator: requested mode not registered, falling back to react",
				zap.String("requested_mode", preference.String()),
			)
		}
	}

	if factory, ok := r.factories[CoordinatorModeReAct]; ok {
		return factory(deps)
	}

	// Truly empty registry — programmer bug, surface loudly.
	panic(fmt.Sprintf("coordinator registry has no react factory; init order broken"))
}

// registerBuiltinCoordinators wires the default modes into a registry. Called
// from NewQueryEngine so every QueryEngine starts with a working ReAct +
// Plan combo. External callers can Register additional modes
// post-construction (e.g. tests adding a mock coordinator).
//
// ReAct is wired with allowEscalation=true so D-mode auto-promotion to
// Plan happens transparently on recoverable failures. Tests that want
// "pure" ReAct behaviour register a no-escalation factory through the
// public registry.Register seam.
func registerBuiltinCoordinators(r *coordinatorRegistry) {
	r.Register(CoordinatorModeReAct, func(deps *SharedDeps) Coordinator {
		return NewReActCoordinator(deps, true)
	})
	r.Register(CoordinatorModePlan, func(deps *SharedDeps) Coordinator {
		return &PlanCoordinator{deps: deps}
	})
}
