package agent

import (
	"strings"
)

// MentionMatch holds the result of parsing a user message for an @agent mention.
type MentionMatch struct {
	AgentName string // name of the matched agent definition
	Message   string // remaining message after stripping the @mention
	Matched   bool   // whether an @mention was found and matched
}

// MentionParser detects @agent-name mentions at the start of user messages
// and resolves them against a registry of known agent definitions.
type MentionParser struct {
	registry *AgentDefinitionRegistry
}

// NewMentionParser creates a MentionParser backed by the given registry.
func NewMentionParser(reg *AgentDefinitionRegistry) *MentionParser {
	return &MentionParser{registry: reg}
}

// Parse examines text for a leading @agent-name mention. If a known agent is
// mentioned at the start, it returns Matched=true with the canonical agent name
// and the remaining message (leading whitespace trimmed). Otherwise it returns
// Matched=false.
func (p *MentionParser) Parse(text string) MentionMatch {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "@") {
		return MentionMatch{}
	}

	// Strip the leading '@'.
	afterAt := trimmed[1:]

	// Try to match against all registered agent names. We iterate all names
	// and pick the longest match to handle cases where one agent name is a
	// prefix of another (e.g., "code" vs "code-reviewer").
	var bestName string
	for _, def := range p.registry.All() {
		name := def.Name
		if len(afterAt) < len(name) {
			continue
		}
		candidate := afterAt[:len(name)]
		if !strings.EqualFold(candidate, name) {
			continue
		}
		// The character after the agent name must be whitespace or end-of-string.
		rest := afterAt[len(name):]
		if len(rest) > 0 && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' {
			continue
		}
		if len(name) > len(bestName) {
			bestName = name
		}
	}

	if bestName == "" {
		return MentionMatch{}
	}

	remaining := strings.TrimSpace(afterAt[len(bestName):])
	return MentionMatch{
		AgentName: bestName,
		Message:   remaining,
		Matched:   true,
	}
}
