package agent

import (
	"testing"
)

func newTestRegistry() *AgentDefinitionRegistry {
	reg := NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	return reg
}

func TestMentionParser_BasicMatch(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@plan design auth strategy")
	if !m.Matched {
		t.Fatal("expected Matched=true")
	}
	if m.AgentName != "plan" {
		t.Fatalf("expected AgentName=plan, got %q", m.AgentName)
	}
	if m.Message != "design auth strategy" {
		t.Fatalf("expected remaining message 'design auth strategy', got %q", m.Message)
	}
}

func TestMentionParser_CaseInsensitive(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@plan look for main.go")
	if !m.Matched {
		t.Fatal("expected Matched=true for case-insensitive match")
	}
	if m.AgentName != "plan" {
		t.Fatalf("expected AgentName=plan, got %q", m.AgentName)
	}
	if m.Message != "look for main.go" {
		t.Fatalf("expected remaining message 'look for main.go', got %q", m.Message)
	}
}

func TestMentionParser_MultiWordAgentName(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@plan_agent do something complex")
	if !m.Matched {
		t.Fatal("expected Matched=true for multi-word agent name")
	}
	if m.AgentName != "plan_agent" {
		t.Fatalf("expected AgentName=plan_agent, got %q", m.AgentName)
	}
	if m.Message != "do something complex" {
		t.Fatalf("expected remaining message 'do something complex', got %q", m.Message)
	}
}

func TestMentionParser_NoMention(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("just a regular message")
	if m.Matched {
		t.Fatal("expected Matched=false for message without @")
	}
	if m.AgentName != "" {
		t.Fatalf("expected empty AgentName, got %q", m.AgentName)
	}
	if m.Message != "" {
		t.Fatalf("expected empty Message, got %q", m.Message)
	}
}

func TestMentionParser_UnknownAgent(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@nonexistent-agent hello")
	if m.Matched {
		t.Fatal("expected Matched=false for unknown agent")
	}
}

func TestMentionParser_MentionInMiddle(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("hello @plan do something")
	if m.Matched {
		t.Fatal("expected Matched=false when @mention is not at start")
	}
}

func TestMentionParser_EmptyString(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("")
	if m.Matched {
		t.Fatal("expected Matched=false for empty string")
	}
}

func TestMentionParser_JustAtSign(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@")
	if m.Matched {
		t.Fatal("expected Matched=false for bare @")
	}
}

func TestMentionParser_AgentNameOnly(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@plan")
	if !m.Matched {
		t.Fatal("expected Matched=true for agent name with no trailing message")
	}
	if m.AgentName != "plan" {
		t.Fatalf("expected AgentName=plan, got %q", m.AgentName)
	}
	if m.Message != "" {
		t.Fatalf("expected empty Message, got %q", m.Message)
	}
}

func TestMentionParser_LeadingWhitespace(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("  @plan find files")
	if !m.Matched {
		t.Fatal("expected Matched=true even with leading whitespace")
	}
	if m.AgentName != "plan" {
		t.Fatalf("expected AgentName=plan, got %q", m.AgentName)
	}
	if m.Message != "find files" {
		t.Fatalf("expected remaining message 'find files', got %q", m.Message)
	}
}

func TestMentionParser_AgentNamePrefix(t *testing.T) {
	// Ensure that "plan_agent" is not matched by a partial prefix like "plan"
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@plan_agent check this PR")
	if !m.Matched {
		t.Fatal("expected Matched=true for plan_agent")
	}
	if m.AgentName != "plan_agent" {
		t.Fatalf("expected AgentName=plan_agent, got %q", m.AgentName)
	}
	if m.Message != "check this PR" {
		t.Fatalf("expected remaining message 'check this PR', got %q", m.Message)
	}
}

func TestMentionParser_AgentNameNotSubstring(t *testing.T) {
	// "@planning" should NOT match "plan" since 'n' follows without whitespace
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@planning something")
	if m.Matched {
		t.Fatal("expected Matched=false when text after agent name continues without whitespace")
	}
}
