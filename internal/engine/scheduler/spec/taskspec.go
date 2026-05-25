// Package spec holds task/agent declaration types. Used as the input to
// scheduler.Submit and embedded in TaskState.LeafSpec for persistence.
package spec

import (
	"time"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// TaskSpec is the input to scheduler.Submit and the persisted task definition.
type TaskSpec struct {
	LocalID   string            `json:"local_id,omitempty"`   // plan-internal alias
	Goal      string            `json:"goal"`                  // natural-language goal
	Hint      Hint              `json:"hint,omitempty"`        // router selection hint
	AgentDef  AgentDef          `json:"agent_def,omitempty"`   // role profile
	Deps      []DepRef          `json:"deps,omitempty"`        // LocalID or absolute TaskID
	Budget    types.Budget      `json:"budget"`
	Resource  types.ResourceReq `json:"resource_req"`
	Priority  int8              `json:"priority"`
	InputRef  types.Ref         `json:"input_ref,omitempty"`
	SessionID string            `json:"session_id,omitempty"`  // workspace session

	// L3 leaf execution params
	Model        string   `json:"model,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Layout       string   `json:"layout,omitempty"` // "flat" | "per-task"

	// InputPaths lists absolute file paths produced by upstream tasks.
	// Passed verbatim into SpawnConfig.InputPaths so the leaf sees a
	// <task-inputs> preamble pointing to the files instead of having
	// the content re-embedded inline.
	InputPaths []string `json:"input_paths,omitempty"`

	// SubagentType pins the agent type for this leaf task, bypassing AgentResolver.
	// Empty → AgentResolver selects based on goal text.
	SubagentType string `json:"subagent_type,omitempty"`

	// Escalation carries context from a prior failed attempt to the next coordinator
	Escalation *EscalationInfo `json:"escalation,omitempty"`
}

// Hint guides router.Select. Kind takes precedence when non-zero.
type Hint struct {
	Kind types.Kind `json:"kind,omitempty"`
}

// DepRef is a dependency reference: a LocalID (plan-internal sibling) or
// an absolute TaskID (already-known parent/sibling).
type DepRef string

// EscalationInfo carries context from a prior failed attempt to the next coordinator.
type EscalationInfo struct {
	FromKind    string    `json:"from_kind"`
	Reason      string    `json:"reason"`
	Failures    []string  `json:"failures,omitempty"`
	EscalatedAt time.Time `json:"escalated_at"`
}

// IsEmpty returns true if the EscalationInfo is nil or has no meaningful content.
func (e *EscalationInfo) IsEmpty() bool {
	if e == nil {
		return true
	}
	return e.Reason == "" && len(e.Failures) == 0
}

