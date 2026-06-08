package prompt

import "strings"

// ToSystemPrompt joins all blocks into a single string.
// This is the Step 1 integration path — behavior-preserving.
// Later steps will use PromptOutput.Blocks directly for structured API calls.
func (po *PromptOutput) ToSystemPrompt() string {
	if po == nil || len(po.Blocks) == 0 {
		return ""
	}

	parts := make([]string, 0, len(po.Blocks))
	for _, b := range po.Blocks {
		if b.Content != "" {
			parts = append(parts, b.Content)
		}
	}

	return strings.Join(parts, "\n\n")
}

// FallbackOutput creates a minimal PromptOutput from a raw string.
// Used when the Builder fails and we need to fall back to a static prompt.
func FallbackOutput(systemPrompt string) *PromptOutput {
	tokens := EstimateTokens(systemPrompt)
	return &PromptOutput{
		Blocks: []PromptBlock{
			{
				Name:            "fallback",
				Content:         systemPrompt,
				Cacheable:       true,
				EstimatedTokens: tokens,
			},
		},
		Version: ComputeVersion([]PromptBlock{{Name: "fallback", Content: systemPrompt}}),
		Metadata: PromptMetadata{
			ProfileName: "fallback",
			TotalTokens: tokens,
			CacheMetrics: CacheMetrics{
				CacheableTokens:     tokens,
				CacheableRatio:      1.0,
				CacheableBlockCount: 1,
			},
		},
	}
}

// DefaultRegistry creates a registry with all built-in sections registered.
func DefaultRegistry() *Registry {
	// Import cycle prevention: sections are registered by the caller.
	// This function creates an empty registry for the caller to populate.
	return NewRegistry()
}
