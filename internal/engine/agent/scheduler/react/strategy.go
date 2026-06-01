// Package react implements the react dispatch strategy used by the
// scheduler L2 module. It is a thin wrapper around the legacy
// enginesched.Coordinator: build a TaskSpec with Hint.Kind=react and
// hand it off. The Coordinator runs the heavy lifting (router selection,
// L3 dispatch through msgbus, ConsumerPool, etc.) and writes the result
// meta.json under the workspace.
//
// Why we don't reimplement the loop here: the legacy Coordinator already
// owns a full ReAct kernel + ConsumerPool stack that handles concurrent
// L3 spawns and forwards their events to the supplied out channel.
// Rebuilding that with loop.Run for Stage 7 would duplicate hundreds of
// lines for no migration benefit — Stage 8 is when the legacy stack
// finally goes away and this strategy will be rewritten to drive
// loop.Run directly with the scheduler tool palette.
package react

import (
	"context"

	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/spec"
	schedulertypes "harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/pkg/types"
)

// Strategy wraps the legacy Coordinator's react path.
type Strategy struct {
	coord *enginesched.Coordinator
}

// New builds a Strategy bound to the given Coordinator.
func New(coord *enginesched.Coordinator) *Strategy {
	return &Strategy{coord: coord}
}

// Run dispatches the goal to the Coordinator with Hint.Kind=react. The
// returned MetaRef points at the meta.json the L3 leaf wrote; the
// caller is responsible for reading it back into a SpawnResult.
//
// outCh is forwarded into the Coordinator so L3 sub-agent lifecycle
// events (start/end, tool calls, intents) reach the parent stream.
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
		Hint:          spec.Hint{Kind: schedulertypes.KindReact},
		ParentAgentID: parentAgentID,
	}
	return s.coord.Run(ctx, sp, outCh)
}
