// Package tstate is the single source of truth for task state.
// Fields are exported with JSON tags so they serialize to SQLite cleanly.
// Cross-package writes are blocked at the lint layer (handlers + scheduler only).
package tstate

import (
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

// TaskState is one row in the tstate.Store.
type TaskState struct {
	// Identity / relations
	ID        types.TaskID `json:"id"`
	TeamID    types.TeamID `json:"team_id"`
	SessionID string       `json:"session_id"`
	ParentID  types.TaskID `json:"parent_id"`
	Kind      types.Kind   `json:"kind"`

	// Scheduling
	Status      types.Status      `json:"status"`
	Priority    int8              `json:"priority"`
	Deps        []types.TaskID    `json:"deps"`
	ResourceReq types.ResourceReq `json:"resource_req"`
	Budget      types.Budget      `json:"budget"`

	// Runtime accounting
	Attempt    int         `json:"attempt"`
	Lease      types.Lease `json:"lease"`
	SpawnDepth int         `json:"spawn_depth"`

	// Content refs
	InputRef        types.Ref      `json:"input_ref"`
	ResultRef       types.Ref      `json:"result_ref"`
	StagedResultRef types.Ref      `json:"staged_result_ref"`
	CheckpointRef   types.Ref      `json:"checkpoint_ref"`
	WaitingFor      []types.TaskID `json:"waiting_for"`

	// Failure
	LastError    string              `json:"last_error"`
	FailedReason types.FailureReason `json:"failed_reason"`

	// Timestamps
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`

	// Embedded spec (persisted for restart recovery / L3 consumer reads details)
	LeafSpec spec.TaskSpec `json:"leaf_spec"`
}
