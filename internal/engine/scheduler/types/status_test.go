// internal/engine/scheduler/types/status_test.go
package types_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestStatusAllValues(t *testing.T) {
	all := []types.Status{
		types.StatusPending, types.StatusReady, types.StatusRunning, types.StatusWaiting,
		types.StatusSucceeded, types.StatusFailed, types.StatusCancelling, types.StatusCancelled,
	}
	if len(all) != 8 {
		t.Fatalf("want 8 statuses, got %d", len(all))
	}
	seen := map[types.Status]bool{}
	for _, s := range all {
		if s == "" {
			t.Fatal("empty status value")
		}
		if seen[s] {
			t.Fatalf("duplicate status %q", s)
		}
		seen[s] = true
	}
}

func TestKindAllValues(t *testing.T) {
	all := []types.Kind{types.KindReact, types.KindPlan, types.KindTeam, types.KindVote, types.KindLeaf}
	if len(all) != 5 {
		t.Fatalf("want 5 kinds, got %d", len(all))
	}
}

func TestStatusIsTerminal(t *testing.T) {
	terminal := []types.Status{types.StatusSucceeded, types.StatusFailed, types.StatusCancelled}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	nonTerminal := []types.Status{types.StatusPending, types.StatusReady, types.StatusRunning, types.StatusWaiting, types.StatusCancelling}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%s should NOT be terminal", s)
		}
	}
}
