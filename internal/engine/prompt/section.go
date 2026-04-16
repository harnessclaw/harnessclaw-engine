package prompt

// Section is the fundamental building block of a system prompt.
// Each Section owns a single concern (role, tools, memory, etc.)
// and knows how to render itself within a given token budget.
type Section interface {
	// Name returns a unique identifier for this section.
	// Used for profile filtering, logging, and cache keying.
	Name() string

	// Priority controls render order. Lower values render first.
	// Recommended ranges:
	//   1-9:    Identity (role, persona)
	//   10-19:  Behavioral rules (principles, output format)
	//   20-29:  Capabilities (tools, skills)
	//   30-39:  Context (env, memory, task)
	//   40-49:  Session-specific (current task, conversation state)
	Priority() int

	// Cacheable returns true if this section's content is stable
	// across turns and can benefit from API-level prompt caching.
	//
	// Rule of thumb:
	//   - true:  role, principles, output format
	//   - false: tools (may vary), env, memory, task
	Cacheable() bool

	// MinTokens is the minimum token budget this section needs
	// to produce useful output. If the allocator cannot grant at
	// least MinTokens, the section is skipped entirely.
	//
	// Return 0 if the section should always attempt to render.
	MinTokens() int

	// Render produces the section content given the prompt context
	// and an allocated token budget.
	//
	// Returns:
	//   - (content, nil):  section rendered successfully
	//   - ("", nil):       section has nothing to contribute (skip)
	//   - ("", err):       section failed; Builder logs and skips
	//
	// The tokenBudget is a soft limit in estimated tokens.
	Render(ctx *PromptContext, tokenBudget int) (string, error)
}
