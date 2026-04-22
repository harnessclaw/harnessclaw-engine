package agent

import (
	"testing"

	"harnessclaw-go/internal/tool"
)

func TestAgentDefinitionRegistry_RegisterAndGet(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	def := &AgentDefinition{
		Name:        "test-agent",
		Description: "A test agent",
		AgentType:   tool.AgentTypeSync,
	}
	reg.Register(def)

	got := reg.Get("test-agent")
	if got == nil {
		t.Fatal("expected to find agent definition")
	}
	if got.Description != "A test agent" {
		t.Errorf("expected description 'A test agent', got %q", got.Description)
	}
}

func TestAgentDefinitionRegistry_GetNil(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	if reg.Get("nonexistent") != nil {
		t.Error("expected nil for unknown agent")
	}
}

func TestAgentDefinitionRegistry_Overwrite(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.Register(&AgentDefinition{Name: "x", Description: "first"})
	reg.Register(&AgentDefinition{Name: "x", Description: "second"})

	got := reg.Get("x")
	if got.Description != "second" {
		t.Errorf("expected overwritten description 'second', got %q", got.Description)
	}
}

func TestAgentDefinitionRegistry_All(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.Register(&AgentDefinition{Name: "a"})
	reg.Register(&AgentDefinition{Name: "b"})

	all := reg.All()
	if len(all) != 2 {
		t.Errorf("expected 2 definitions, got %d", len(all))
	}
}

func TestAgentDefinitionRegistry_Names(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.Register(&AgentDefinition{Name: "alpha"})
	reg.Register(&AgentDefinition{Name: "beta"})

	names := reg.Names()
	if len(names) != 2 {
		t.Errorf("expected 2 names, got %d", len(names))
	}
	// Check both names exist
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["alpha"] || !nameSet["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestAgentDefinitionRegistry_RegisterBuiltins(t *testing.T) {
	reg := NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()

	if reg.Get("general-purpose") == nil {
		t.Error("expected 'general-purpose' builtin")
	}
	if reg.Get("Explore") == nil {
		t.Error("expected 'Explore' builtin")
	}
	if reg.Get("Plan") == nil {
		t.Error("expected 'Plan' builtin")
	}
}
