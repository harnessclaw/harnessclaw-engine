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

// BudgetAwareSection is an optional interface that sections can implement
// to participate in demand-driven budget allocation. Sections that implement
// this declare how many tokens they ideally want, allowing the allocator to
// satisfy demand exactly when budget permits and trim intelligently when it doesn't.
//
// Sections that do NOT implement this fall back to tier-based static allocation.
type BudgetAwareSection interface {
	Section

	// IdealTokens returns the number of tokens this section would use
	// if given unlimited budget, for the given context.
	// This should be a fast estimate (not a full render).
	// Return 0 to indicate "use whatever the tier allocates".
	IdealTokens(ctx *PromptContext) int
}
