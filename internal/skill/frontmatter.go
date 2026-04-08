package skill

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringOrSlice is a custom type that accepts both a single YAML string
// and a YAML sequence. A single string is split on ", " to produce a slice.
// This handles SKILL.md files where allowed-tools can be either:
//
//	allowed-tools: Bash(npm run:*), Bash(npx:*)     # string
//	allowed-tools:                                    # sequence
//	  - Bash(npm run:*)
//	  - Bash(npx:*)
type StringOrSlice []string

// UnmarshalYAML implements yaml.Unmarshaler for StringOrSlice.
func (s *StringOrSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// Single string — split on ", " to produce slice.
		raw := value.Value
		if raw == "" {
			*s = nil
			return nil
		}
		parts := strings.Split(raw, ", ")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				result = append(result, p)
			}
		}
		*s = result
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		*s = items
		return nil
	default:
		return fmt.Errorf("allowed-tools: expected string or sequence, got kind %d", value.Kind)
	}
}

// Frontmatter holds parsed YAML frontmatter from a SKILL.md file.
// Mirrors parseSkillFrontmatterFields() from src/skills/loadSkillsDir.ts.
type Frontmatter struct {
	// Name overrides the directory-derived name.
	Name string `yaml:"name"`
	// Description is shown to users and the model.
	Description string `yaml:"description"`
	// WhenToUse provides detailed usage scenarios.
	WhenToUse string `yaml:"when_to_use"`
	// Arguments defines named argument placeholders (e.g., ["file", "pattern"]).
	Arguments StringOrSlice `yaml:"arguments"`
	// Aliases are alternative names.
	Aliases StringOrSlice `yaml:"aliases"`
	// ArgumentHint is displayed in gray after the command name.
	ArgumentHint string `yaml:"argument-hint"`
	// AllowedTools restricts which tools the model can use.
	AllowedTools StringOrSlice `yaml:"allowed-tools"`
	// Model overrides the model for this skill.
	Model string `yaml:"model"`
	// Effort overrides the reasoning effort level.
	Effort string `yaml:"effort"`
	// Context is "" for inline, "fork" for sub-agent execution.
	Context string `yaml:"context"`
	// Agent specifies the agent type for forked execution.
	Agent string `yaml:"agent"`
	// DisableModelInvocation prevents the model from invoking via SkillTool.
	DisableModelInvocation bool `yaml:"disable-model-invocation"`
	// UserInvocable indicates if users can invoke via /command-name.
	UserInvocable *bool `yaml:"user-invocable"`
	// Version is the skill version string.
	Version string `yaml:"version"`
	// Paths are glob patterns for conditional activation.
	Paths StringOrSlice `yaml:"paths"`
	// Shell specifies the shell for shell commands in the prompt.
	Shell string `yaml:"shell"`
}

// ParseFrontmatter extracts and parses YAML frontmatter from markdown content.
// Frontmatter is delimited by "---" lines at the start of the file.
// Returns the parsed frontmatter, the remaining body, and any error.
func ParseFrontmatter(content string) (*Frontmatter, string, error) {
	fm := &Frontmatter{}

	// Check for frontmatter delimiter.
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		// No frontmatter — entire content is the body.
		return fm, content, nil
	}

	// Find the closing delimiter.
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		// No closing delimiter — treat as no frontmatter.
		return fm, content, nil
	}

	fmContent := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:]) // skip "\n---"

	if err := yaml.Unmarshal([]byte(fmContent), fm); err != nil {
		return nil, "", err
	}

	return fm, body, nil
}
