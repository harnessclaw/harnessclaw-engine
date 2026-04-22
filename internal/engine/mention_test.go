package engine

import (
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
)

func newTestRegistry() *agent.AgentDefinitionRegistry {
	reg := agent.NewAgentDefinitionRegistry()
	reg.Register(&agent.AgentDefinition{
		Name:      "explorer",
		AgentType: tool.AgentTypeSync,
	})
	reg.Register(&agent.AgentDefinition{
		Name:      "Plan",
		AgentType: tool.AgentTypeSync,
	})
	return reg
}

func TestMentionParser_NoMention(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("just plain text")
	if r.AgentName != "" {
		t.Errorf("expected empty AgentName, got %q", r.AgentName)
	}
	if r.Prompt != "just plain text" {
		t.Errorf("expected original text as Prompt, got %q", r.Prompt)
	}
}

func TestMentionParser_MatchedAgent(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("@explorer analyze this code")
	if r.AgentName != "explorer" {
		t.Errorf("expected AgentName 'explorer', got %q", r.AgentName)
	}
	if r.Prompt != "analyze this code" {
		t.Errorf("expected Prompt 'analyze this code', got %q", r.Prompt)
	}
}

func TestMentionParser_UnmatchedAgent(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("@unknown do something")
	if r.AgentName != "" {
		t.Errorf("expected empty AgentName for unmatched mention, got %q", r.AgentName)
	}
	if r.Prompt != "@unknown do something" {
		t.Errorf("expected original text as Prompt, got %q", r.Prompt)
	}
}

func TestMentionParser_LeadingWhitespace(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("  @explorer look at this")
	if r.AgentName != "explorer" {
		t.Errorf("expected AgentName 'explorer', got %q", r.AgentName)
	}
	if r.Prompt != "look at this" {
		t.Errorf("expected Prompt 'look at this', got %q", r.Prompt)
	}
}

func TestMentionParser_MentionOnly(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("@explorer")
	if r.AgentName != "explorer" {
		t.Errorf("expected AgentName 'explorer', got %q", r.AgentName)
	}
	if r.Prompt != "" {
		t.Errorf("expected empty Prompt, got %q", r.Prompt)
	}
}

func TestMentionParser_NilRegistry(t *testing.T) {
	p := NewMentionParser(nil)
	r := p.Parse("@explorer hello")
	if r.AgentName != "" {
		t.Errorf("expected empty AgentName with nil registry, got %q", r.AgentName)
	}
	if r.Prompt != "@explorer hello" {
		t.Errorf("expected original text as Prompt, got %q", r.Prompt)
	}
}

func TestMentionParser_AtSignAlone(t *testing.T) {
	p := NewMentionParser(newTestRegistry())
	r := p.Parse("@")
	if r.AgentName != "" {
		t.Errorf("expected empty AgentName for bare @, got %q", r.AgentName)
	}
	if r.Prompt != "@" {
		t.Errorf("expected original text '@' as Prompt, got %q", r.Prompt)
	}
}
