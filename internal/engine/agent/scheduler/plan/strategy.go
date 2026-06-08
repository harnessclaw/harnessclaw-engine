// Package plan implements the plan dispatch strategy used by the
// scheduler L2 module. It is a thin wrapper around agentrun: build a
// TaskSpec with Hint.Kind=plan and hand it off via
// agentrun.ModeScheduled. The runner forwards the call to the wired
// SchedulerBackend (currently enginesched.Coordinator) which drives
// the two-phase plan_agent → guard → plan_executor_agent flow inside
// internal/engine/scheduler/dispatch/plan.
//
// Migrated from direct enginesched.Coordinator.Run as part of the
// agentrun unification (P4).
package plan

import (
	"context"

	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	schedulertypes "harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/pkg/types"
)

// Strategy dispatches plan-mode tasks through the agentrun runner.
type Strategy struct {
	rt *agentrun.Runner
}

// New builds a Strategy bound to the given agentrun.Runner. The runner
// must be constructed with WithScheduler(...) wired to a Coordinator.
func New(rt *agentrun.Runner) *Strategy {
	return &Strategy{rt: rt}
}

// Run dispatches the goal through agentrun.ModeScheduled with
// Hint.Kind=plan. outCh is forwarded into the scheduler so both
// phases' L3 events reach the parent stream.
func (s *Strategy) Run(
	ctx context.Context,
	goal string,
	sessionID string,
	model string,
	outCh chan<- types.EngineEvent,
	parentAgentID string,
) (schedulertypes.MetaRef, error) {
	sp := spec.TaskSpec{
		Goal:          goal,
		Layout:        "flat",
		SessionID:     sessionID,
		Model:         model,
		Hint:          spec.Hint{Kind: schedulertypes.KindPlan},
		ParentAgentID: parentAgentID,
	}
	res, err := s.rt.Run(ctx, agentrun.Request{
		Spec:   &sp,
		Mode:   agentrun.ModeScheduled,
		Events: outCh,
	})
	if err != nil {
		return "", err
	}
	return res.MetaRef, nil
}
