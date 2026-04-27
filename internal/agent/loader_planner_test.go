package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlanner_FromUserDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	dir := filepath.Join(home, ".harnessclaw", "workspace", "agents")
	if _, err := os.Stat(filepath.Join(dir, "planner.yaml")); os.IsNotExist(err) {
		t.Skip("planner.yaml not found, skipping")
	}

	reg := NewAgentDefinitionRegistry()
	if err := reg.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory failed: %v", err)
	}

	def := reg.Get("planner")
	if def == nil {
		t.Fatal("planner agent not found in registry")
	}

	if def.DisplayName != "Planner" {
		t.Errorf("DisplayName = %q, want %q", def.DisplayName, "Planner")
	}
	if def.AgentType != "sync" {
		t.Errorf("AgentType = %q, want %q", def.AgentType, "sync")
	}
	if def.Profile != "plan" {
		t.Errorf("Profile = %q, want %q", def.Profile, "plan")
	}
	if def.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want %d", def.MaxTurns, 20)
	}
	if len(def.AllowedTools) == 0 {
		t.Error("AllowedTools is empty, expected non-empty")
	}
	if def.SystemPrompt == "" {
		t.Error("SystemPrompt is empty")
	}
	if len(def.Skills) != 3 {
		t.Errorf("Skills count = %d, want 3", len(def.Skills))
	}
	expectedSkills := map[string]bool{"review": true, "security-review": true, "init": true}
	for _, s := range def.Skills {
		if !expectedSkills[s] {
			t.Errorf("unexpected skill: %s", s)
		}
	}

	t.Logf("planner loaded: %d allowed tools, %d skills, %d char system prompt",
		len(def.AllowedTools), len(def.Skills), len(def.SystemPrompt))
}
