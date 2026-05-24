// Package react implements the single-leaf react dispatch strategy (§5.3.2).
// One call to SpawnAndWaitOne fires a KindLeaf sub-task and blocks for its result.
package react

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// Strategy implements dispatch.Strategy for the "react" kind.
// It spawns exactly one KindLeaf child and returns its MetaRef.
type Strategy struct {
	caps dispatch.Capabilities
}

// New creates a Strategy with the given Capabilities.
// If caps.LeafKind is empty it defaults to "react-leaf".
func New(caps dispatch.Capabilities) *Strategy {
	if caps.LeafKind == "" {
		caps.LeafKind = "react-leaf"
	}
	return &Strategy{caps: caps}
}

func (Strategy) Kind() types.Kind                    { return types.KindReact }
func (r *Strategy) Capabilities() dispatch.Capabilities { return r.caps }

// Run spawns one KindLeaf child, waits for its KindResult, and returns the
// MetaRef. On non-"done" status it returns *dispatch.LeafFailedError.
//
// v3.1-R6: SpawnAndWaitOne subscribes BEFORE publishing the control{spawn},
// so there is no race between grant/result and the subscription.
func (r *Strategy) Run(ctx context.Context, taskID types.TaskID, deps dispatch.Deps) (types.MetaRef, error) {
	task, err := deps.Reader.Get(ctx, taskID)
	if err != nil {
		return "", err
	}

	leafSpec := spec.TaskSpec{
		LocalID:      "leaf-0",
		Goal:         task.LeafSpec.Goal,
		Hint:         spec.Hint{Kind: types.KindLeaf},
		AllowedTools: r.caps.AllowedTools,
		Model:        task.LeafSpec.Model,
		Layout:       "flat",
		AgentDef:     task.LeafSpec.AgentDef,
		SessionID:    task.SessionID,
	}

	res, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID, leafSpec)
	if err != nil {
		return "", err
	}
	if res.Status != "done" {
		return "", &dispatch.LeafFailedError{Reason: res.Reason}
	}

	// EscalateHook placeholder (phase 2.2 fills real escalation)
	if r.caps.EscalateHook != nil {
		_ = r.caps.EscalateHook(dispatch.EscalateState{})
	}
	return types.MetaRef(res.OutputFile), nil
}
