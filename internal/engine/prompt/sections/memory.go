package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
)

// MemorySection renders user memory entries.
type MemorySection struct{}

func NewMemorySection() *MemorySection {
	return &MemorySection{}
}

func (s *MemorySection) Name() string     { return "memory" }
func (s *MemorySection) Priority() int    { return 31 }
func (s *MemorySection) Cacheable() bool  { return false }
func (s *MemorySection) MinTokens() int   { return 30 }

func (s *MemorySection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if len(ctx.Memory) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("# Memory\n\n")
	sb.WriteString("User preferences and context:\n\n")

	// Estimate if we need to truncate
	fullContent := s.buildFullContent(ctx.Memory)
	if prompt.EstimateTokens(fullContent) <= budget {
		return fullContent, nil
	}

	// Truncate to fit budget
	return s.buildTruncatedContent(ctx.Memory, budget), nil
}

func (s *MemorySection) buildFullContent(memory map[string]string) string {
	var sb strings.Builder
	sb.WriteString("# Memory\n\n")
	sb.WriteString("User preferences and context:\n\n")

	for key, value := range memory {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", key, value))
	}

	return sb.String()
}

func (s *MemorySection) buildTruncatedContent(memory map[string]string, budget int) string {
	var sb strings.Builder
	sb.WriteString("# Memory\n\n")

	// Simple truncation: include entries until budget exhausted
	remaining := budget - 50 // Reserve for header
	for key, value := range memory {
		entry := fmt.Sprintf("- %s: %s\n", key, value)
		entryTokens := prompt.EstimateTokens(entry)
		if entryTokens <= remaining {
			sb.WriteString(entry)
			remaining -= entryTokens
		}
		if remaining <= 0 {
			break
		}
	}

	return sb.String()
}
