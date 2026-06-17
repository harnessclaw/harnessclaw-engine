package builtin_test

import (
	"slices"
	"testing"

	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/engine/agent/definition"
)

func TestRegisterAll_RegistersAllBuiltins(t *testing.T) {
	reg := definition.NewRegistry()
	if err := builtin.RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	for _, name := range []string{"plan", "freelancer", "content_creator"} {
		if reg.Get(name) == nil {
			t.Errorf("expected %q to be registered", name)
		}
	}
}

func TestFreelancer_BuiltinRegistration(t *testing.T) {
	reg := definition.NewRegistry()
	if err := builtin.RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	def := reg.Get("freelancer")
	if def == nil {
		t.Fatal("freelancer builtin not registered")
	}
	if def.EffectiveTier() != definition.TierSubAgent {
		t.Errorf("Tier = %s, want sub_agent", def.EffectiveTier())
	}
	if len(def.OutputSchema) == 0 {
		t.Error("OutputSchema must be set for TierSubAgent")
	}
	if !def.IsTeamMember {
		t.Error("freelancer must be a team member so emma can dispatch to it")
	}
	// AllowedTools must contain the four skill self-management tools
	wantTools := []string{"search_skill", "load_skill", "unload_skill", "list_loaded_skills"}
	for _, w := range wantTools {
		if !slices.Contains(def.AllowedTools, w) {
			t.Errorf("AllowedTools missing %q", w)
		}
	}
	// Must NOT contain "skill" tool — spec calls this out explicitly
	if slices.Contains(def.AllowedTools, "skill") {
		t.Error("freelancer AllowedTools should NOT contain Skill tool (use LoadSkill)")
	}

	// Regression: InputSchema must NOT require `task` because Task tool
	// puts the task description in cfg.Prompt, not cfg.Inputs. Requiring
	// it here would break every freelancer dispatch that includes
	// candidate_skills (cfg.Inputs becomes non-empty → schema validates →
	// finds no `task` → fails).
	if req, ok := def.InputSchema["required"]; ok {
		if arr, ok := req.([]string); ok {
			for _, r := range arr {
				if r == "task" {
					t.Errorf("InputSchema.required must NOT contain %q — task text "+
						"flows through cfg.Prompt not cfg.Inputs; this would break "+
						"every dispatch with candidate_skills", r)
				}
			}
		}
	}
	// Properties should still describe candidate_skills (for L2 to know
	// the maxItems constraint).
	if props, ok := def.InputSchema["properties"].(map[string]any); ok {
		if _, hasCS := props["candidate_skills"]; !hasCS {
			t.Error("InputSchema.properties should describe candidate_skills")
		}
	}
}
