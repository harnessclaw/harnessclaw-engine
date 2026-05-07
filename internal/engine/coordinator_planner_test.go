package engine

import (
	"context"
	"strings"
	"testing"
)

// HeuristicPlanner v1.16+: only decomposes "what to do"; does NOT bind
// executors. Tests focus on step count + rationale; executor tests live
// in coordinator_subagent_resolver_test.go.

func TestHeuristicPlanner_ResearchThenWrite(t *testing.T) {
	p := NewHeuristicPlanner()
	out, err := p.Plan(context.Background(), PlannerInput{
		Goal:               "调研大模型推理优化的最新进展，写一篇 3 章节的报告",
		AvailableSubagents: []string{"researcher", "writer", "analyst"},
	})
	if err != nil {
		t.Fatalf("planner returned error: %v", err)
	}
	if got := len(out.Plan.Steps); got != 2 {
		t.Fatalf("research+write should produce 2 steps, got %d (%s)", got, out.Rationale)
	}
	// Steps now carry no SubagentType — Scheduler resolves at dispatch.
	for i, s := range out.Plan.Steps {
		if s.SubagentType != "" {
			t.Errorf("step %d should leave SubagentType empty (resolver picks at dispatch); got %q",
				i, s.SubagentType)
		}
	}
	if !strings.Contains(out.Rationale, "research+write") {
		t.Errorf("rationale should mention pattern; got %q", out.Rationale)
	}
	if len(out.Plan.Steps[1].DependsOn) != 1 || out.Plan.Steps[1].DependsOn[0] != out.Plan.Steps[0].ID {
		t.Errorf("step 2 should depend on step 1, got DependsOn=%v", out.Plan.Steps[1].DependsOn)
	}
	if err := out.Plan.Validate(); err != nil {
		t.Errorf("planner produced invalid plan: %v", err)
	}
}

func TestHeuristicPlanner_ResearchThenAnalyze(t *testing.T) {
	p := NewHeuristicPlanner()
	out, err := p.Plan(context.Background(), PlannerInput{
		Goal:               "调研三家电动车续航数据，对比一下",
		AvailableSubagents: []string{"researcher", "analyst", "writer"},
	})
	if err != nil {
		t.Fatalf("planner error: %v", err)
	}
	if len(out.Plan.Steps) != 2 {
		t.Fatalf("research+analyze should produce 2 steps; got %d", len(out.Plan.Steps))
	}
	if !strings.Contains(out.Rationale, "research+analyze") {
		t.Errorf("rationale should mention pattern; got %q", out.Rationale)
	}
}

func TestHeuristicPlanner_DefaultsToSingleStep(t *testing.T) {
	p := NewHeuristicPlanner()
	out, err := p.Plan(context.Background(), PlannerInput{
		Goal:               "翻译这段英文",
		AvailableSubagents: []string{"writer", "researcher"},
	})
	if err != nil {
		t.Fatalf("planner error: %v", err)
	}
	if len(out.Plan.Steps) != 1 {
		t.Fatalf("simple task should produce 1 step; got %d", len(out.Plan.Steps))
	}
	if out.Plan.Steps[0].SubagentType != "" {
		t.Errorf("step should not pre-bind executor; got %q", out.Plan.Steps[0].SubagentType)
	}
}

func TestHeuristicPlanner_RejectsEmptyGoal(t *testing.T) {
	p := NewHeuristicPlanner()
	if _, err := p.Plan(context.Background(), PlannerInput{}); err == nil {
		t.Error("empty goal should fail")
	}
}

func TestHeuristicPlanner_RejectsNoAvailableSubagents(t *testing.T) {
	p := NewHeuristicPlanner()
	if _, err := p.Plan(context.Background(), PlannerInput{Goal: "anything"}); err == nil {
		t.Error("no available subagents should fail")
	}
}
