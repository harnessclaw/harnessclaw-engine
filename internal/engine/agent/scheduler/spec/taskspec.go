// Package spec holds task/agent declaration types. Used as the input to
// scheduler.Submit and embedded in TaskState.LeafSpec for persistence.
package spec

import (
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/types"
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

	// ParentAgentID is the dispatching L2 agent's session id, plumbed
	// from the L2 module through the strategy so QueryEngineFactory can
	// stamp it on each L3 SpawnConfig. The translator then parents the
	// L3 card under this L2's card; without it L3 falls back to the
	// grandparent (emma's tool call) and the UI renders L2/L3 as
	// siblings rather than nested.
	//
	// Not persisted: this is a runtime wiring hint, not part of the
	// recorded task definition.
	ParentAgentID string `json:"-"`
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

