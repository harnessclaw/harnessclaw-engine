package plan

import (
	"strings"
	"testing"
	"time"
)

func TestBudgetTracker_Unlimited(t *testing.T) {
	tr := NewBudgetTracker(Budget{})
	for i := 0; i < 100; i++ {
		if got := tr.ConsumeStep(); got != "" {
			t.Fatalf("unlimited tracker fired at step %d: %q", i, got)
		}
		if got := tr.RecordFailure(); got != "" {
			t.Fatalf("unlimited tracker failure-fired at %d: %q", i, got)
		}
	}
}

func TestBudgetTracker_MaxSteps(t *testing.T) {
	tr := NewBudgetTracker(Budget{MaxSteps: 3})
	for i := 0; i < 3; i++ {
		if got := tr.ConsumeStep(); got != "" {
			t.Fatalf("step %d unexpectedly capped: %q", i, got)
		}
	}
	got := tr.ConsumeStep()
	if !strings.Contains(got, "max_steps") {
		t.Fatalf("4th step should hit max_steps, got %q", got)
	}
}

func TestBudgetTracker_MaxFailures(t *testing.T) {
	tr := NewBudgetTracker(Budget{MaxFailures: 2})
	if got := tr.RecordFailure(); got != "" {
		t.Fatalf("1st failure should pass, got %q", got)
	}
	got := tr.RecordFailure()
	if !strings.Contains(got, "max_failures") {
		t.Fatalf("2nd failure should hit max_failures, got %q", got)
	}
}

func TestBudgetTracker_Deadline(t *testing.T) {
	tr := NewBudgetTracker(Budget{Deadline: time.Now().Add(-1 * time.Second)})
	got := tr.Exceeded()
	if !strings.Contains(got, "deadline") {
		t.Fatalf("past deadline should fire, got %q", got)
	}
}

func TestBudgetTracker_Snapshot(t *testing.T) {
	tr := NewBudgetTracker(Budget{MaxSteps: 10})
	_ = tr.ConsumeStep()
	_ = tr.ConsumeStep()
	_ = tr.RecordFailure()
	s := tr.Snapshot()
	if s.Steps != 2 {
		t.Errorf("steps = %d, want 2", s.Steps)
	}
	if s.Failures != 1 {
		t.Errorf("failures = %d, want 1", s.Failures)
	}
	if s.Cap.MaxSteps != 10 {
		t.Errorf("cap.MaxSteps = %d, want 10", s.Cap.MaxSteps)
	}
}

func TestBudget_IsZero(t *testing.T) {
	if !(Budget{}).IsZero() {
		t.Errorf("empty Budget should be zero")
	}
	if (Budget{MaxSteps: 1}).IsZero() {
		t.Errorf("Budget with MaxSteps should not be zero")
	}
}
