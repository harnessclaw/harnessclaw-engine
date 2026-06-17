package prompt

import (
	"testing"
)

// 注：旧 TestPlannerProfile_RegisteredInBuiltins /
// TestPlannerProfile_HasJSONSchemaInPrompt 已删 —— PlannerProfile 本身
// 已删（orchestrate 工具孤儿）。

func TestResolveProfileBySubagentType(t *testing.T) {
	tests := []struct{ in, want string }{
		{"researcher", "explore"},
		{"plan", "plan"},
		{"freelancer", "freelancer"},
		{"content_creator", "content_creator"},
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
