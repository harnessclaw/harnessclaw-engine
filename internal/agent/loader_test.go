package agent

import (
	"os"
	"path/filepath"
	"testing"

	"harnessclaw-go/internal/tool"
)

func TestLoadFromDirectory_ValidYAML(t *testing.T) {
	dir := t.TempDir()

	yamlContent := `
name: test-agent
display_name: Test Agent
description: A test agent for unit tests
system_prompt: You are a test agent.
agent_type: sync
profile: full
model: claude-3-opus
max_turns: 10
tools:
  - Read
  - Write
allowed_tools:
  - Bash
disallowed_tools:
  - WebFetch
auto_team: false
`
	if err := os.WriteFile(filepath.Join(dir, "test-agent.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write yaml file: %v", err)
	}

	reg := NewAgentDefinitionRegistry()
	if err := reg.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory returned error: %v", err)
	}

	def := reg.Get("test-agent")
	if def == nil {
		t.Fatal("expected agent definition 'test-agent' to be registered")
	}

	if def.DisplayName != "Test Agent" {
		t.Errorf("DisplayName = %q, want %q", def.DisplayName, "Test Agent")
	}
	if def.Description != "A test agent for unit tests" {
		t.Errorf("Description = %q, want %q", def.Description, "A test agent for unit tests")
	}
	if def.SystemPrompt != "You are a test agent." {
		t.Errorf("SystemPrompt = %q, want %q", def.SystemPrompt, "You are a test agent.")
	}
	if def.AgentType != tool.AgentTypeSync {
		t.Errorf("AgentType = %q, want %q", def.AgentType, tool.AgentTypeSync)
	}
	if def.Profile != "full" {
		t.Errorf("Profile = %q, want %q", def.Profile, "full")
	}
	if def.Model != "claude-3-opus" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-3-opus")
	}
	if def.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want %d", def.MaxTurns, 10)
	}
	if len(def.Tools) != 2 || def.Tools[0] != "Read" || def.Tools[1] != "Write" {
		t.Errorf("Tools = %v, want [Read Write]", def.Tools)
	}
	if len(def.AllowedTools) != 1 || def.AllowedTools[0] != "Bash" {
		t.Errorf("AllowedTools = %v, want [Bash]", def.AllowedTools)
	}
	if len(def.DisallowedTools) != 1 || def.DisallowedTools[0] != "WebFetch" {
		t.Errorf("DisallowedTools = %v, want [WebFetch]", def.DisallowedTools)
	}
	if def.AutoTeam != false {
		t.Errorf("AutoTeam = %v, want false", def.AutoTeam)
	}
	if def.Source != filepath.Join(dir, "test-agent.yaml") {
		t.Errorf("Source = %q, want %q", def.Source, filepath.Join(dir, "test-agent.yaml"))
	}
}

func TestLoadFromDirectory_NonExistent(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	err := reg.LoadFromDirectory("/tmp/does-not-exist-harnessclaw-test-dir")
	if err != nil {
		t.Fatalf("expected nil error for non-existent directory, got: %v", err)
	}
}

func TestLoadFromDirectory_MissingName(t *testing.T) {
	dir := t.TempDir()

	yamlContent := `
description: An agent with no name
agent_type: sync
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write yaml file: %v", err)
	}

	reg := NewAgentDefinitionRegistry()
	err := reg.LoadFromDirectory(dir)
	if err == nil {
		t.Fatal("expected error for YAML missing 'name', got nil")
	}
}

func TestLoadFromDirectory_SubAgents(t *testing.T) {
	dir := t.TempDir()

	yamlContent := `
name: team-lead
display_name: Team Lead
description: A coordinator with sub-agents
agent_type: coordinator
auto_team: true
sub_agents:
  - name: researcher
    role: Research code and gather context
    agent_type: sync
    profile: explore
  - name: implementer
    role: Write code changes
    agent_type: teammate
    profile: full
`
	if err := os.WriteFile(filepath.Join(dir, "team-lead.yml"), []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write yaml file: %v", err)
	}

	reg := NewAgentDefinitionRegistry()
	if err := reg.LoadFromDirectory(dir); err != nil {
		t.Fatalf("LoadFromDirectory returned error: %v", err)
	}

	def := reg.Get("team-lead")
	if def == nil {
		t.Fatal("expected agent definition 'team-lead' to be registered")
	}

	if def.AgentType != tool.AgentTypeCoordinator {
		t.Errorf("AgentType = %q, want %q", def.AgentType, tool.AgentTypeCoordinator)
	}
	if !def.AutoTeam {
		t.Error("AutoTeam = false, want true")
	}

	if len(def.SubAgents) != 2 {
		t.Fatalf("len(SubAgents) = %d, want 2", len(def.SubAgents))
	}

	sa0 := def.SubAgents[0]
	if sa0.Name != "researcher" {
		t.Errorf("SubAgents[0].Name = %q, want %q", sa0.Name, "researcher")
	}
	if sa0.Role != "Research code and gather context" {
		t.Errorf("SubAgents[0].Role = %q, want %q", sa0.Role, "Research code and gather context")
	}
	if sa0.AgentType != tool.AgentTypeSync {
		t.Errorf("SubAgents[0].AgentType = %q, want %q", sa0.AgentType, tool.AgentTypeSync)
	}
	if sa0.Profile != "explore" {
		t.Errorf("SubAgents[0].Profile = %q, want %q", sa0.Profile, "explore")
	}

	sa1 := def.SubAgents[1]
	if sa1.Name != "implementer" {
		t.Errorf("SubAgents[1].Name = %q, want %q", sa1.Name, "implementer")
	}
	if sa1.AgentType != tool.AgentTypeTeammate {
		t.Errorf("SubAgents[1].AgentType = %q, want %q", sa1.AgentType, tool.AgentTypeTeammate)
	}
	if sa1.Profile != "full" {
		t.Errorf("SubAgents[1].Profile = %q, want %q", sa1.Profile, "full")
	}
}
