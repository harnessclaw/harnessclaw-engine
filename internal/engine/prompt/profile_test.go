package prompt

import (
	"strings"
	"testing"
)

func TestPlannerProfile_RegisteredInBuiltins(t *testing.T) {
	profiles := GetBuiltInProfiles()
	p, ok := profiles["planner"]
	if !ok {
		t.Fatal("planner profile not registered in GetBuiltInProfiles")
	}
	if p.Name != "planner" {
		t.Errorf("profile.Name = %q, want planner", p.Name)
	}
	if p == EmmaProfile || p == WorkerProfile {
		t.Error("planner profile must not be aliased to emma/worker")
	}
}

func TestResolveProfileBySubagentType_Planner(t *testing.T) {
	tests := []struct{ in, want string }{
		{"planner", "planner"},
		{"Planner", "planner"},
		{"researcher", "explore"},
		{"Plan", "plan"},
		{"writer", "worker"},
		{"unknown", "worker"},
	}
	for _, tt := range tests {
		got := ResolveProfileBySubagentType(tt.in)
		if got.Name != tt.want {
			t.Errorf("ResolveProfileBySubagentType(%q).Name = %q, want %q", tt.in, got.Name, tt.want)
		}
	}
}

func TestPlannerProfile_HasJSONSchemaInPrompt(t *testing.T) {
	// Sanity check: the planner role/principles must mention the plan JSON
	// fields so the agent knows what to emit.
	override := PlannerProfile.SectionOverrides["principles"]
	for _, must := range []string{"step_id", "subagent_type", "depends_on", "<summary>"} {
		if !strings.Contains(override, must) {
			t.Errorf("planner principles missing %q", must)
		}
	}
}
