package prompt

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// PromptBlock is a single logical block in the system prompt.
type PromptBlock struct {
	// Name identifies which Section produced this block.
	Name string

	// Content is the rendered text.
	Content string

	// Cacheable indicates this block's content is stable and
	// the provider should mark it for prompt caching if supported.
	Cacheable bool

	// EstimatedTokens is the approximate token count of Content.
	EstimatedTokens int
}

// PromptOutput is the structured result of Builder.Build().
type PromptOutput struct {
	// Blocks are the rendered sections in priority order.
	Blocks []PromptBlock

	// Version is a content-hash of all blocks, for tracing.
	Version string

	// Metadata captures build-time decisions for observability.
	Metadata PromptMetadata
}

// PromptMetadata records what happened during prompt assembly.
type PromptMetadata struct {
	// ProfileName is the agent profile used.
	ProfileName string

	// TotalTokens is the sum of all block EstimatedTokens.
	TotalTokens int

	// TokenBudget is the total budget that was available.
	TokenBudget int

	// SkippedSections lists sections that were excluded and why.
	SkippedSections []SkipRecord

	// SectionTokens maps section name to [allocated, used] tokens.
	SectionTokens map[string][2]int

	// CacheMetrics tracks caching potential.
	CacheMetrics CacheMetrics

	// BuildDuration is how long Build() took.
	BuildDuration time.Duration
}

// SkipRecord explains why a section was not rendered.
type SkipRecord struct {
	Section string
	Reason  string // "empty", "budget", "error", "disabled"
}

// CacheMetrics tracks prompt caching potential.
type CacheMetrics struct {
	// CacheableTokens is the total tokens in cacheable blocks.
	CacheableTokens int

	// DynamicTokens is the total tokens in non-cacheable blocks.
	DynamicTokens int

	// CacheableRatio is CacheableTokens / TotalTokens.
	CacheableRatio float64

	// CacheableBlockCount is the number of cacheable blocks.
	CacheableBlockCount int
}

// ComputeVersion produces a short content hash of all rendered blocks.
func ComputeVersion(blocks []PromptBlock) string {
	h := sha256.New()
	for _, b := range blocks {
		h.Write([]byte(b.Name))
		h.Write([]byte{0})
		h.Write([]byte(b.Content))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("p_%x", h.Sum(nil)[:8])
}

// Dump produces a human-readable representation for debugging.
func (po *PromptOutput) Dump() string {
	var result string
	result += fmt.Sprintf("=== Prompt %s ===\n", po.Version)
	result += fmt.Sprintf("Profile: %s | Budget: %d | Used: %d\n\n",
		po.Metadata.ProfileName,
		po.Metadata.TokenBudget,
		po.Metadata.TotalTokens,
	)

	for i, b := range po.Blocks {
		cache := ""
		if b.Cacheable {
			cache = " [CACHEABLE]"
		}
		result += fmt.Sprintf("--- Block %d: %s (~%d tokens)%s ---\n",
			i+1, b.Name, b.EstimatedTokens, cache)
		result += b.Content
		result += "\n\n"
	}

	if len(po.Metadata.SkippedSections) > 0 {
		result += "--- Skipped ---\n"
		for _, s := range po.Metadata.SkippedSections {
			result += fmt.Sprintf("  %s: %s\n", s.Section, s.Reason)
		}
	}

	return result
}
