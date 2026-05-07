package engine

import (
	"strings"
	"testing"
)

func TestPlan_Validate_HappyPath(t *testing.T) {
	p := &Plan{
		Goal: "research X then write Y",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher", Prompt: "research X"},
			{ID: "s2", SubagentType: "writer", Prompt: "write Y", DependsOn: []string{"s1"}},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
}

func TestPlan_Validate_RejectsCycles(t *testing.T) {
	p := &Plan{
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer", DependsOn: []string{"s2"}},
			{ID: "s2", SubagentType: "writer", DependsOn: []string{"s1"}},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("cyclic plan should be rejected")
	}
}

func TestPlan_Validate_RejectsForwardDeps(t *testing.T) {
	p := &Plan{
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer", DependsOn: []string{"s2"}},
			{ID: "s2", SubagentType: "writer"},
		},
	}
	err := p.Validate()
	if err == nil {
		t.Fatalf("forward dep should be flagged; got nil")
	}
	if !strings.Contains(err.Error(), "forward") && !strings.Contains(err.Error(), "later step") {
		t.Fatalf("error should mention forward / later step; got %v", err)
	}
}

func TestPlan_Validate_RejectsDuplicateIDs(t *testing.T) {
	p := &Plan{
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer"},
			{ID: "s1", SubagentType: "writer"},
		},
	}
	if err := p.Validate(); err == nil {
		t.Fatal("duplicate ID should be rejected")
	}
}

func TestPlan_Validate_RejectsEmptyPlan(t *testing.T) {
	if err := (&Plan{}).Validate(); err == nil {
		t.Error("empty plan should be rejected")
	}
}

func TestPlan_Validate_AllowsEmptySubagentType(t *testing.T) {
	// v1.16+: SubagentType is optional; the Scheduler resolves it via
	// SubagentResolver at dispatch time. Validate must not reject a
	// step that leaves it empty — that's the new default for plans
	// produced by HeuristicPlanner.
	p := &Plan{Goal: "x", Steps: []*PlanStep{{ID: "s1"}}}
	if err := p.Validate(); err != nil {
		t.Errorf("empty SubagentType should be accepted (resolver picks later); got %v", err)
	}
}

func TestPlan_Find(t *testing.T) {
	p := &Plan{Steps: []*PlanStep{
		{ID: "a", SubagentType: "writer"},
		{ID: "b", SubagentType: "writer"},
	}}
	if p.Find("b") == nil {
		t.Error("Find should return existing step")
	}
	if p.Find("missing") != nil {
		t.Error("Find should return nil for missing")
	}
}

func TestPlan_TopologicalOrder_PreservesInputOrder(t *testing.T) {
	// Planner contract: Steps are emitted in topo order. The accessor
	// just exposes them; the test pins that contract so a future
	// "smart" reorder can't silently break the Scheduler.
	p := &Plan{Steps: []*PlanStep{
		{ID: "a", SubagentType: "writer"},
		{ID: "b", SubagentType: "writer", DependsOn: []string{"a"}},
		{ID: "c", SubagentType: "writer", DependsOn: []string{"b"}},
	}}
	got := p.TopologicalOrder()
	want := []string{"a", "b", "c"}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("at %d: got %q, want %q", i, got[i], id)
		}
	}
}
