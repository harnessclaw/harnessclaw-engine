package plan

import (
	"fmt"
	"time"
)

// Budget caps a plan-strategy run. Zero-value fields mean "no cap".
//
// MaxSteps     — hard cap on the number of step-spawns (each Phase-2
//                iteration counts as one).
// MaxFailures  — total failed steps tolerated before the run is aborted.
// Deadline     — absolute wall-clock at which the run halts (whether or
//                not steps remain).
type Budget struct {
	MaxSteps    int
	MaxFailures int
	Deadline    time.Time
}

// IsZero reports whether no cap is configured.
func (b Budget) IsZero() bool {
	return b.MaxSteps == 0 && b.MaxFailures == 0 && b.Deadline.IsZero()
}

// BudgetTracker accumulates spend during a Strategy.Run and reports when a
// cap is exceeded. Not safe for concurrent use; the plan strategy iterates
// sequentially.
type BudgetTracker struct {
	cfg      Budget
	steps    int
	failures int
	started  time.Time
}

// NewBudgetTracker constructs a tracker with the given Budget. A zero Budget
// disables all caps.
func NewBudgetTracker(cfg Budget) *BudgetTracker {
	return &BudgetTracker{cfg: cfg, started: time.Now()}
}

// ConsumeStep is called immediately before a step is dispatched. It records
// the step and returns "" if the run may proceed, or a non-empty reason
// string when a cap has been reached.
func (t *BudgetTracker) ConsumeStep() string {
	t.steps++
	return t.checkLimits()
}

// RecordFailure marks one failed step. Returns "" if the run may proceed,
// or a reason string when the failure cap has been reached.
func (t *BudgetTracker) RecordFailure() string {
	t.failures++
	return t.checkLimits()
}

// Exceeded reports the first cap that has been hit, or "" if budget is OK.
// Useful at the top of a loop iteration to short-circuit before any work.
func (t *BudgetTracker) Exceeded() string {
	return t.checkLimits()
}

func (t *BudgetTracker) checkLimits() string {
	if t.cfg.MaxSteps > 0 && t.steps > t.cfg.MaxSteps {
		return fmt.Sprintf("max_steps exceeded (%d > %d)", t.steps, t.cfg.MaxSteps)
	}
	if t.cfg.MaxFailures > 0 && t.failures >= t.cfg.MaxFailures {
		return fmt.Sprintf("max_failures exceeded (%d >= %d)", t.failures, t.cfg.MaxFailures)
	}
	if !t.cfg.Deadline.IsZero() && time.Now().After(t.cfg.Deadline) {
		return fmt.Sprintf("deadline exceeded (at %s)", t.cfg.Deadline.Format(time.RFC3339))
	}
	return ""
}

// Snapshot returns a copy of the current counters for diagnostics / emit
// envelopes. The returned struct never mutates with the tracker.
type BudgetSnapshot struct {
	Steps    int
	Failures int
	Elapsed  time.Duration
	Cap      Budget
}

// Snapshot returns the current state of the tracker.
func (t *BudgetTracker) Snapshot() BudgetSnapshot {
	return BudgetSnapshot{
		Steps:    t.steps,
		Failures: t.failures,
		Elapsed:  time.Since(t.started),
		Cap:      t.cfg,
	}
}
