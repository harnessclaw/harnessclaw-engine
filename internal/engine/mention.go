package engine

import (
	"harnessclaw-go/internal/agent"
)

// MentionParser extracts @agent_name references from user messages.
// It delegates to agent.MentionParser for longest-match resolution.
type MentionParser struct {
	inner *agent.MentionParser
	reg   *agent.AgentDefinitionRegistry
}

// NewMentionParser creates a parser backed by an agent definition registry.
func NewMentionParser(reg *agent.AgentDefinitionRegistry) *MentionParser {
	var inner *agent.MentionParser
	if reg != nil {
		inner = agent.NewMentionParser(reg)
	}
	return &MentionParser{inner: inner, reg: reg}
}

// MentionResult contains a parsed @-mention and the remaining prompt text.
type MentionResult struct {
	// AgentName is the matched agent name (empty if no match).
	AgentName string
	// Prompt is the user message text with the @mention removed.
	Prompt string
}

// Parse extracts the first @mention from the beginning of text.
// Rules:
//  1. Only parses @mention at the start of the message (leading whitespace ok).
//  2. @mention must match a registered agent definition name.
//  3. Unmatched @xxx is left as-is (returned in Prompt, AgentName empty).
func (p *MentionParser) Parse(text string) *MentionResult {
	if p.inner == nil {
		return &MentionResult{Prompt: text}
	}

	match := p.inner.Parse(text)
	if !match.Matched {
		return &MentionResult{Prompt: text}
	}

	return &MentionResult{
		AgentName: match.AgentName,
		Prompt:    match.Message,
	}
}
