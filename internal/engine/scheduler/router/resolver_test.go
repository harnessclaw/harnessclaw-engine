package router_test

import (
	"testing"
	"harnessclaw-go/internal/engine/scheduler/router"
)

func TestHeuristicAgentResolver_ResearchGoal(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"researcher", "developer", "writer", "freelancer"}
	got := r.Resolve("research the impact of LLMs on software engineering", available)
	if got != "researcher" {
		t.Fatalf("want researcher, got %q", got)
	}
}

func TestHeuristicAgentResolver_WriteGoal(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"writer", "researcher", "freelancer"}
	got := r.Resolve("write a blog post about Go generics", available)
	if got != "writer" {
		t.Fatalf("want writer, got %q", got)
	}
}

func TestHeuristicAgentResolver_FallbackToFreelancer(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"freelancer"}
	got := r.Resolve("do something vague", available)
	if got != "freelancer" {
		t.Fatalf("want freelancer, got %q", got)
	}
}

func TestHeuristicAgentResolver_EmptyAvailable(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	got := r.Resolve("research something", nil)
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
