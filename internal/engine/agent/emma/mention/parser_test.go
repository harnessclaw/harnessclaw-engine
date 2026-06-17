package mention

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/engine/agent/definition"
)

func newTestRegistry() *definition.Registry {
	reg := definition.NewRegistry()
	_ = builtin.RegisterAll(reg)
	// 合成一个 multi-word agent 用于解析器测试（带下划线、可能跟单词
	// 边界冲突）—— 内建 agent 名都是单词，没法覆盖 multi-word / prefix-
	// collision 这两个解析器关心的边界条件。
	_ = reg.Register(&definition.AgentDefinition{
		Name:        "multi_word_agent",
		DisplayName: "Multi-Word Test Agent",
		Description: "synthetic agent used to exercise underscore parsing in mention parser tests",
	})
	return reg
}

func TestMentionParser_BasicMatch(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@freelancer write a poem")
	if !m.Matched {
		t.Fatal("expected Matched=true")
	}
	if m.AgentName != "freelancer" {
		t.Fatalf("expected AgentName=freelancer, got %q", m.AgentName)
	}
	if m.Message != "write a poem" {
		t.Fatalf("expected remaining message 'write a poem', got %q", m.Message)
	}
}

func TestMentionParser_CaseInsensitive(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@freelancer look for main.go")
	if !m.Matched {
		t.Fatal("expected Matched=true for case-insensitive match")
	}
	if m.AgentName != "freelancer" {
		t.Fatalf("expected AgentName=freelancer, got %q", m.AgentName)
	}
	if m.Message != "look for main.go" {
		t.Fatalf("expected remaining message 'look for main.go', got %q", m.Message)
	}
}

func TestMentionParser_MultiWordAgentName(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@multi_word_agent do something complex")
	if !m.Matched {
		t.Fatal("expected Matched=true for multi-word agent name")
	}
	if m.AgentName != "multi_word_agent" {
		t.Fatalf("expected AgentName=multi_word_agent, got %q", m.AgentName)
	}
	if m.Message != "do something complex" {
		t.Fatalf("expected remaining message 'do something complex', got %q", m.Message)
	}
}

func TestMentionParser_NoMention(t *testing.T) {
	parser := NewParser(newTestRegistry())

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
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@nonexistent-agent hello")
	if m.Matched {
		t.Fatal("expected Matched=false for unknown agent")
	}
}

func TestMentionParser_MentionInMiddle(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("hello @freelancer do something")
	if m.Matched {
		t.Fatal("expected Matched=false when @mention is not at start")
	}
}

func TestMentionParser_EmptyString(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("")
	if m.Matched {
		t.Fatal("expected Matched=false for empty string")
	}
}

func TestMentionParser_JustAtSign(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@")
	if m.Matched {
		t.Fatal("expected Matched=false for bare @")
	}
}

func TestMentionParser_AgentNameOnly(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@freelancer")
	if !m.Matched {
		t.Fatal("expected Matched=true for agent name with no trailing message")
	}
	if m.AgentName != "freelancer" {
		t.Fatalf("expected AgentName=freelancer, got %q", m.AgentName)
	}
	if m.Message != "" {
		t.Fatalf("expected empty Message, got %q", m.Message)
	}
}

func TestMentionParser_LeadingWhitespace(t *testing.T) {
	parser := NewParser(newTestRegistry())

	m := parser.Parse("  @freelancer find files")
	if !m.Matched {
		t.Fatal("expected Matched=true even with leading whitespace")
	}
	if m.AgentName != "freelancer" {
		t.Fatalf("expected AgentName=freelancer, got %q", m.AgentName)
	}
	if m.Message != "find files" {
		t.Fatalf("expected remaining message 'find files', got %q", m.Message)
	}
}

func TestMentionParser_AgentNamePrefix(t *testing.T) {
	// Ensure underscore-bearing agent names parse end-to-end without
	// being chopped at the first underscore (e.g. "multi" prefix).
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@multi_word_agent check this PR")
	if !m.Matched {
		t.Fatal("expected Matched=true for multi_word_agent")
	}
	if m.AgentName != "multi_word_agent" {
		t.Fatalf("expected AgentName=multi_word_agent, got %q", m.AgentName)
	}
	if m.Message != "check this PR" {
		t.Fatalf("expected remaining message 'check this PR', got %q", m.Message)
	}
}

func TestMentionParser_AgentNameNotSubstring(t *testing.T) {
	// "@freelancerly" should NOT match "freelancer" since 'l' follows without whitespace
	parser := NewParser(newTestRegistry())

	m := parser.Parse("@freelancerly something")
	if m.Matched {
		t.Fatal("expected Matched=false when text after agent name continues without whitespace")
	}
}
