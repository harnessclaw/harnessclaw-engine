// Package context assembles the system prompt and user context for LLM calls.
package context

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
)

// Builder assembles system prompts from multiple sources.
type Builder struct {
	cfg *config.Config
}

// NewBuilder creates a context builder.
func NewBuilder(cfg *config.Config) *Builder {
	return &Builder{cfg: cfg}
}

// Part is a named section of the system prompt.
type Part struct {
	Name    string
	Content string
	Order   int
}

// BuildSystemPrompt assembles the full system prompt from parts.
func (b *Builder) BuildSystemPrompt(parts []Part) string {
	// Sort by order
	sorted := make([]Part, len(parts))
	copy(sorted, parts)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Order < sorted[i].Order {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	var sb strings.Builder
	for _, p := range sorted {
		if p.Content == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("# %s\n\n%s\n\n", p.Name, p.Content))
	}
	return sb.String()
}
