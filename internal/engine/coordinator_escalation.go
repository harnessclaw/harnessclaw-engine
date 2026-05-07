package engine

import (
	"strings"
	"time"

	"harnessclaw-go/pkg/types"
)

// EscalationContext is the carry-over a coordinator hands to its successor
// when promoting a failing run instead of starting from scratch. Mirrors
// architecture_v2.md §"图 1" — fast path → L2 escalation, and the
// per-Plan-mode re-plan loop.
//
// Two callers create it:
//   1. ReActCoordinator on terminal failure → constructs Escalation with
//      the partial artifacts already produced and the failure reasons,
//      then dispatches a PlanCoordinator carrying it
//   2. PlanCoordinator on review_goal miss → re-plans, handing the
//      already-produced step results forward so the new plan can skip
//      finished work
//
// The Planner consumes Escalation via PlannerInput.Escalation.
type EscalationContext struct {
	// FromMode tells the next coordinator who is escalating. Useful for
	// logging and for the Planner to decide how aggressive to be (a
	// react→plan handoff is "we tried the cheap route, now plan
	// carefully"; a plan→plan re-plan is "we already planned, fix the
	// gap").
	FromMode CoordinatorMode

	// Reason is a short human-readable diagnosis emitted into trace
	// events and surfaced in fallback summaries.
	Reason string

	// PriorAttempts records each L3 sub-agent run the predecessor made,
	// successful or not. The Planner uses this to avoid redoing work.
	PriorAttempts []PriorAttempt

	// PriorArtifacts is the union of artifact refs already produced and
	// available in the artifact store. The new coordinator treats them
	// as "already done" inputs — the Scheduler skips steps whose outputs
	// match a prior artifact role.
	PriorArtifacts []types.ArtifactRef

	// BudgetSpent is what the predecessor consumed; the successor's
	// BudgetTracker starts from this baseline so the overall task
	// budget is shared, not reset.
	BudgetSpent BudgetSnapshot

	// Failures lists structured failure reasons from the predecessor,
	// most recent first. Empty when the escalation is voluntary
	// (e.g., explicit upgrade because the user asked).
	Failures []string

	// EscalatedAt anchors the handoff time so re-plan jitter logging
	// can compute "time spent since the original task started".
	EscalatedAt time.Time
}

// IsEmpty reports whether ec carries no useful state — used by the Planner
// to skip the "incorporate prior work" branch entirely.
func (ec *EscalationContext) IsEmpty() bool {
	if ec == nil {
		return true
	}
	return len(ec.PriorAttempts) == 0 &&
		len(ec.PriorArtifacts) == 0 &&
		len(ec.Failures) == 0
}

// FormatForLog produces a single-line summary suitable for zap.String
// fields. Avoids dumping full artifact refs (which can blow up log
// lines); just the count and the first few IDs.
func (ec *EscalationContext) FormatForLog() string {
	if ec.IsEmpty() {
		return "(empty)"
	}
	var b strings.Builder
	b.WriteString("from=")
	b.WriteString(string(ec.FromMode))
	b.WriteString(" attempts=")
	b.WriteString(itoa(len(ec.PriorAttempts)))
	b.WriteString(" artifacts=")
	b.WriteString(itoa(len(ec.PriorArtifacts)))
	if ec.Reason != "" {
		b.WriteString(" reason=\"")
		b.WriteString(truncForLog(ec.Reason, 80))
		b.WriteString("\"")
	}
	return b.String()
}

// PriorAttempt is one previously-executed sub-agent invocation. Recording
// these lets the Planner say "step X already produced art_yyy; skip it"
// instead of re-spending tokens.
type PriorAttempt struct {
	Skill     string
	Prompt    string
	Status    string // "success" | "failed" | "partial"
	Artifacts []types.ArtifactRef
	Failures  []string
}

// itoa is a tiny int→string helper kept local so the escalation file
// doesn't pull in strconv (the only other use site).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// truncForLog clips s at n runes with ellipsis. Local helper to avoid the
// rune-budget logic showing up in three files.
func truncForLog(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 4 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
