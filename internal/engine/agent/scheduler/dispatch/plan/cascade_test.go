package plan

import (
	"reflect"
	"testing"

	"harnessclaw-go/internal/legacy/workspace"
)

func newPlan(tasks map[string]*workspace.Task) *workspace.Plan {
	return &workspace.Plan{SessionID: "sess", Tasks: tasks}
}

func TestCascadeSkip_LinearChain(t *testing.T) {
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A", Agent: "x", Status: workspace.StatusPending},
		"b": {Title: "B", Agent: "x", Status: workspace.StatusPending, DependsOn: []string{"a"}},
		"c": {Title: "C", Agent: "x", Status: workspace.StatusPending, DependsOn: []string{"b"}},
	})
	got := CascadeSkip(pl, "a")
	want := []string{"b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cascade = %v, want %v", got, want)
	}
	for _, id := range want {
		if pl.Tasks[id].Status != workspace.StatusCancelled {
			t.Errorf("task %s status = %s, want cancelled", id, pl.Tasks[id].Status)
		}
	}
}

func TestCascadeSkip_PreservesRunning(t *testing.T) {
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A", Status: workspace.StatusFailed},
		"b": {Title: "B", Status: workspace.StatusRunning, DependsOn: []string{"a"}},
		"c": {Title: "C", Status: workspace.StatusPending, DependsOn: []string{"b"}},
	})
	got := CascadeSkip(pl, "a")
	if len(got) != 1 || got[0] != "c" {
		t.Errorf("cascade = %v, want [c]", got)
	}
	if pl.Tasks["b"].Status != workspace.StatusRunning {
		t.Errorf("running task b should not be skipped (got %s)", pl.Tasks["b"].Status)
	}
}

func TestCascadeSkip_NoDependents(t *testing.T) {
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A", Status: workspace.StatusFailed},
		"b": {Title: "B", Status: workspace.StatusPending}, // unrelated
	})
	got := CascadeSkip(pl, "a")
	if len(got) != 0 {
		t.Errorf("expected no cascades, got %v", got)
	}
	if pl.Tasks["b"].Status != workspace.StatusPending {
		t.Errorf("unrelated task b mutated: %s", pl.Tasks["b"].Status)
	}
}

func TestCascadeSkip_DiamondShape(t *testing.T) {
	// a → b, a → c, b → d, c → d   ⇒  fail a → b, c, d all cancelled
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A", Status: workspace.StatusFailed},
		"b": {Title: "B", Status: workspace.StatusPending, DependsOn: []string{"a"}},
		"c": {Title: "C", Status: workspace.StatusPending, DependsOn: []string{"a"}},
		"d": {Title: "D", Status: workspace.StatusPending, DependsOn: []string{"b", "c"}},
	})
	got := CascadeSkip(pl, "a")
	want := []string{"b", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cascade = %v, want %v", got, want)
	}
}

func TestTopoOrder_Simple(t *testing.T) {
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A"},
		"b": {Title: "B", DependsOn: []string{"a"}},
		"c": {Title: "C", DependsOn: []string{"b"}},
	})
	got, err := TopoOrder(pl)
	if err != nil {
		t.Fatalf("topo error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topo = %v, want %v", got, want)
	}
}

func TestTopoOrder_DeterministicTieBreak(t *testing.T) {
	// All independent; topo should be alphabetic.
	pl := newPlan(map[string]*workspace.Task{
		"b": {Title: "B"},
		"a": {Title: "A"},
		"c": {Title: "C"},
	})
	got, _ := TopoOrder(pl)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topo = %v, want %v", got, want)
	}
}

func TestTopoOrder_CycleDetected(t *testing.T) {
	pl := newPlan(map[string]*workspace.Task{
		"a": {Title: "A", DependsOn: []string{"b"}},
		"b": {Title: "B", DependsOn: []string{"a"}},
	})
	if _, err := TopoOrder(pl); err == nil {
		t.Fatalf("expected cycle error")
	}
}

func TestTopoOrder_EmptyPlan(t *testing.T) {
	got, err := TopoOrder(newPlan(nil))
	if err != nil || got != nil {
		t.Errorf("empty plan should yield (nil, nil), got (%v, %v)", got, err)
	}
}
