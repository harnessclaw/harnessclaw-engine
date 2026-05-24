// Package react implements the single-leaf react dispatch strategy (§5.3.2).
// One call to SpawnAndWaitOne fires a KindLeaf sub-task and blocks for its result.
package react

import (
	"context"
	"strings"

	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
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

	if r.caps.EscalateHook != nil {
		if r.caps.EscalateHook(dispatch.EscalateState{
			Result: msgbus.AgentMessage{
				Kind:    msgbus.KindResult,
				TaskID:  string(taskID),
				Payload: res,
			},
		}) {
			return "", &dispatch.EscalationRequestedError{TaskID: taskID}
		}
	}
	return types.MetaRef(res.OutputFile), nil
}

// ShouldEscalateFromResult decides whether a leaf result warrants escalating
// the task to a higher-level planner. It mirrors the logic of the old
// shouldEscalate(subAgentLoopResult) but operates on msgbus.ResultMessage,
// which is the only data available at the dispatch layer.
//
// Rules:
//   - Status "done" → never escalate (clean success).
//   - Status "cancelled" → never escalate (user-initiated abort).
//   - Status "failed": parse the terminal reason code (left of ":"), then:
//   - "max_turns", "model_error", "blocking_limit" → escalate.
//   - "aborted_streaming", "aborted_tools", "prompt_too_long" → no escalate.
//   - unknown codes → no escalate (conservative default).
func ShouldEscalateFromResult(res msgbus.ResultMessage) bool {
	if res.Status == msgbus.ResultStatusDone || res.Status == msgbus.ResultStatusCancelled {
		return false
	}
	// Parse terminal reason code from "<code>: <free text>" or bare "<code>".
	code := strings.TrimSpace(strings.SplitN(res.Reason, ":", 2)[0])
	switch code {
	case "max_turns", "model_error", "blocking_limit":
		return true
	case "aborted_streaming", "aborted_tools", "prompt_too_long":
		return false
	default:
		return false
	}
}
