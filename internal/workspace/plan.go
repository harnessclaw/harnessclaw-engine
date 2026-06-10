package workspace

import (
	"fmt"
	"time"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusDone, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Plan is the on-disk state machine maintained by L2. Serialised to
// {sessionRoot}/plan.json. Single source of truth for task progress.
type Plan struct {
	SessionID    string             `json:"session_id"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at,omitempty"`
	Tasks        map[string]*Task   `json:"tasks"`
	Deliverables []DeliverableEntry `json:"deliverables,omitempty"`
}

// Task is one work unit dispatched to an L3.
type Task struct {
	Title      string    `json:"title"`
	Agent      string    `json:"agent"`
	Status     Status    `json:"status"`
	Attempt    int       `json:"attempt"`
	DependsOn  []string  `json:"depends_on,omitempty"`
	InputPaths []string  `json:"input_paths,omitempty"`
	OutputDir  string    `json:"output_dir"`
	Frozen     bool      `json:"frozen"`
	SummaryRef string    `json:"summary_ref,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
}

// DeliverableEntry records one promote action.
type DeliverableEntry struct {
	Path         string    `json:"path"`
	PromotedFrom string    `json:"promoted_from"`
	PromotedAt   time.Time `json:"promoted_at"`
}

// Validate checks all invariants on a single Plan snapshot.
func (p *Plan) Validate() error {
	if p.SessionID == "" {
		return fmt.Errorf("plan: empty session_id")
	}
	for id, t := range p.Tasks {
		if t.Title == "" {
			return fmt.Errorf("plan: task %q title is required", id)
		}
		if t.Agent == "" {
			return fmt.Errorf("plan: task %q agent is required", id)
		}
		if !t.Status.Valid() {
			return fmt.Errorf("plan: task %q status %q invalid (want pending|running|done|failed|cancelled)", id, t.Status)
		}
		if t.Status == StatusDone && t.SummaryRef == "" {
			return fmt.Errorf("plan: task %q status=done but summary_ref empty", id)
		}
		for _, dep := range t.DependsOn {
			if _, ok := p.Tasks[dep]; !ok {
				return fmt.Errorf("plan: task %q depends on %q which is not in plan", id, dep)
			}
		}
	}
	return nil
}

// ValidateTransitionFrom checks transition-level invariants between two Plan
// snapshots, then calls Validate on the incoming plan. Callers do not need to
// call Validate separately.
//
// Currently enforces frozen-irreversibility: once a Task has Frozen=true it
// may not be un-frozen or deleted in a subsequent snapshot.
func (p *Plan) ValidateTransitionFrom(old *Plan) error {
	// Check frozen-irreversibility first so that error surfaces before
	// per-task snapshot invariants (e.g. summary_ref) which belong to a
	// separate layer of validation.
	for id, oldT := range old.Tasks {
		if oldT.Frozen {
			newT, ok := p.Tasks[id]
			if !ok || !newT.Frozen {
				return fmt.Errorf("plan: task %q frozen is irreversible (cannot un-freeze or delete)", id)
			}
		}
	}
	return p.Validate()
}
