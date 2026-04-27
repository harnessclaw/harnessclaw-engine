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

	m := parser.Parse("@Explore search for auth files")
	if !m.Matched {
		t.Fatal("expected Matched=true")
	}
	if m.AgentName != "Explore" {
		t.Fatalf("expected AgentName=Explore, got %q", m.AgentName)
	}
	if m.Message != "search for auth files" {
		t.Fatalf("expected remaining message 'search for auth files', got %q", m.Message)
	}
}

func TestMentionParser_CaseInsensitive(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@explore look for main.go")
	if !m.Matched {
		t.Fatal("expected Matched=true for case-insensitive match")
	}
	if m.AgentName != "Explore" {
		t.Fatalf("expected AgentName=Explore, got %q", m.AgentName)
	}
	if m.Message != "look for main.go" {
		t.Fatalf("expected remaining message 'look for main.go', got %q", m.Message)
	}
}

func TestMentionParser_MultiWordAgentName(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@general-purpose do something complex")
	if !m.Matched {
		t.Fatal("expected Matched=true for multi-word agent name")
	}
	if m.AgentName != "general-purpose" {
		t.Fatalf("expected AgentName=general-purpose, got %q", m.AgentName)
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

	m := parser.Parse("hello @Explore do something")
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

	m := parser.Parse("@Plan")
	if !m.Matched {
		t.Fatal("expected Matched=true for agent name with no trailing message")
	}
	if m.AgentName != "Plan" {
		t.Fatalf("expected AgentName=Plan, got %q", m.AgentName)
	}
	if m.Message != "" {
		t.Fatalf("expected empty Message, got %q", m.Message)
	}
}

func TestMentionParser_LeadingWhitespace(t *testing.T) {
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("  @Explore find files")
	if !m.Matched {
		t.Fatal("expected Matched=true even with leading whitespace")
	}
	if m.AgentName != "Explore" {
		t.Fatalf("expected AgentName=Explore, got %q", m.AgentName)
	}
	if m.Message != "find files" {
		t.Fatalf("expected remaining message 'find files', got %q", m.Message)
	}
}

func TestMentionParser_AgentNamePrefix(t *testing.T) {
	// Ensure that "general-purpose" is not matched by a partial prefix like "general"
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@general-purpose check this PR")
	if !m.Matched {
		t.Fatal("expected Matched=true for general-purpose")
	}
	if m.AgentName != "general-purpose" {
		t.Fatalf("expected AgentName=general-purpose, got %q", m.AgentName)
	}
	if m.Message != "check this PR" {
		t.Fatalf("expected remaining message 'check this PR', got %q", m.Message)
	}
}

func TestMentionParser_AgentNameNotSubstring(t *testing.T) {
	// "@Planning" should NOT match "Plan" since 'n' follows without whitespace
	parser := NewMentionParser(newTestRegistry())

	m := parser.Parse("@Planning something")
	if m.Matched {
		t.Fatal("expected Matched=false when text after agent name continues without whitespace")
	}
}
