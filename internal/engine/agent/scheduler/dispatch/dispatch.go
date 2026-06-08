// Package dispatch holds the pluggable scheduling strategies (react/plan/team/vote)
// and their shared interfaces.
package dispatch

import (
	"context"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	pkgtypes "harnessclaw-go/pkg/types"
)

// Emitter publishes progress events to an observer (typically the L1 outCh
// wired through Coordinator.Run). Strategies must treat a nil Emitter as a
// silent sink — call sites should use EmitterFunc(nil).Emit safely.
type Emitter interface {
	Emit(evt pkgtypes.EngineEvent)
}

// EmitterFunc adapts a plain function (or nil channel) into the Emitter
// interface. A nil EmitterFunc is a no-op.
type EmitterFunc func(pkgtypes.EngineEvent)

// Emit forwards the event when the underlying function is non-nil.
func (f EmitterFunc) Emit(evt pkgtypes.EngineEvent) {
	if f == nil {
		return
	}
	f(evt)
}

// Strategy is a pluggable scheduling strategy. Each implements a different approach
// to dispatch and coordinate L3 sub-agents (e.g., react: 1 leaf, plan: N sagas).
type Strategy interface {
	// Kind returns the strategy kind (e.g., "react", "plan", "team", "vote").
	Kind() types.Kind

	// Capabilities returns the worker capability constraints for L3 (allowed tools,
	// spawn depth, escalation hook, etc.).
	Capabilities() Capabilities

	// Run executes the strategy for the given task. Returns the MetaRef pointing to
	// the result (e.g., "meta.json" for react, "tasks/<tid>/meta.json" for plan).
	Run(ctx context.Context, taskID types.TaskID, deps Deps) (types.MetaRef, error)
}

// Deps holds the dependencies injected into every strategy.
// v3.1-R2: Does NOT contain Kernel—dispatch.Strategy has no Writer permissions.
// Strategies dispatch sub-agents via control{spawn} → onSpawn saga through the Bus.
type Deps struct {
	// Reader is the read-only view of tstate.
	Reader tstate.Reader

	// Bus is the message bus: dispatches children (control{spawn}), subscribes to
	// KindResult and KindNotify, and sends control/notify messages.
	// This is the ONLY path for sub-agent coordination.
	Bus msgbus.Bus // msgbus.Publish, SubscribeOnce, Query used here

	// Staging writes StagedResultRef before lifecycle{completed} is published,
	// enabling the reaper fallback if the task crashes.
	Staging tstate.StagingWriter

	// Emitter is an optional sink for progress events (plan_created /
	// step_started / step_completed / step_failed / step_skipped /
	// plan_completed). When nil, strategies must skip event emission.
	// Coordinator wires it from the per-Run outCh channel.
	Emitter Emitter
}

// Capabilities describes the runtime constraints and hooks for a strategy's L3 workers.
type Capabilities struct {
	// AllowedTools is the whitelist of tools passed to L3 as LeafContext.Tools.
	AllowedTools []string

	// AllowSubmit indicates whether the strategy (e.g., plan) can dispatch child sub-agents.
	AllowSubmit bool

	// MaxSpawnDepth limits nested spawning depth.
	MaxSpawnDepth int

	// LeafKind is the default kind for leaf tasks (e.g., "react-leaf").
	LeafKind string

	// EscalateHook is an optional extension point (used by react to promote results to plan).
	// If present, called after L3 returns a result to decide escalation.
	EscalateHook func(EscalateState) bool

	// IdempotentRun marks whether the strategy is idempotent, affecting retry policy.
	IdempotentRun bool

	// RootDir is the workspace root directory used to resolve file paths.
	// Required by the plan strategy to derive the absolute path to plan.json.
	RootDir string

	// PlanMaxSteps caps the number of step-spawns the plan strategy may
	// dispatch in one run (0 = unlimited).
	PlanMaxSteps int

	// PlanMaxFailures aborts the plan strategy after this many step failures
	// (0 = unlimited; failure cap is independent of cascadeSkip).
	PlanMaxFailures int
}

// EscalateState provides context for the EscalateHook decision.
type EscalateState struct {
	// Result is the KindResult AgentMessage from L3.
	// The Payload field contains the msgbus.ResultMessage.
	Result msgbus.AgentMessage
}

// LeafFailedError is returned by react/plan strategies when a leaf sub-task
// finishes with a non-"done" status.
type LeafFailedError struct {
	Reason string
}

func (e *LeafFailedError) Error() string {
	if e.Reason == "" {
		return "leaf task failed"
	}
	return "leaf task failed: " + e.Reason
}

// EscalationRequestedError is returned by react.Strategy when its EscalateHook
// returns true, signalling the parent should re-dispatch via plan mode.
type EscalationRequestedError struct {
	TaskID types.TaskID
}

func (e *EscalationRequestedError) Error() string {
	return "escalation requested for task " + string(e.TaskID)
}
