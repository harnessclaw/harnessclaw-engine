// Package plan implements the plan dispatch strategy used by the
// scheduler L2 module.
//
// During Stage 7 this is a thin wrapper around the legacy
// enginesched.Coordinator: build a TaskSpec with Hint.Kind=plan and
// hand it off. The Coordinator's internal plan strategy (in
// internal/engine/scheduler/dispatch/plan/strategy.go) does the actual
// two-phase work — plan_agent (writes plan.json) → guard
// (requireNonEmptyPlan reads {workspace}/session/{sid}/plan.json) →
// plan_executor_agent (dispatches freelancers via the freelance tool,
// integrates results). Because plan_agent and plan_executor_agent are
// already routed through spawn in Stages 4–5, those leaf dispatches
// still flow through the new module path even though the L2
// orchestration of the two phases lives in the legacy package.
//
// Stage 8 will replace this wrapper with an in-module re-port that uses
// the injected *spawn.Spawner directly, dropping the msgbus →
// QueryEngineFactory → SpawnSync round-trip entirely.
package plan

import (
	"context"

	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/spec"
	schedulertypes "harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/pkg/types"
)

// Strategy wraps the legacy Coordinator's plan path.
type Strategy struct {
	coord *enginesched.Coordinator
}

// New builds a Strategy bound to the given Coordinator.
func New(coord *enginesched.Coordinator) *Strategy {
	return &Strategy{coord: coord}
}

// Run dispatches the goal to the Coordinator with Hint.Kind=plan. The
// Coordinator's internal plan dispatch.Strategy handles phase 1
// (plan_agent) → guard → phase 2 (plan_executor_agent). Returns the
// MetaRef of the final integrated result.
//
// outCh is forwarded into the Coordinator so both phases' L3 events
// reach the parent stream.
func (s *Strategy) Run(
	ctx context.Context,
	goal string,
	sessionID string,
	model string,
	outCh chan<- types.EngineEvent,
) (schedulertypes.MetaRef, error) {
	sp := spec.TaskSpec{
		Goal:      goal,
		Layout:    "flat",
		SessionID: sessionID,
		Model:     model,
		Hint:      spec.Hint{Kind: schedulertypes.KindPlan},
	}
	return s.coord.Run(ctx, sp, outCh)
}
