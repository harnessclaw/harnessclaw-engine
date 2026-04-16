package prompt

const (
	// CharsPerToken is the approximate chars-per-token conversion factor.
	// Conservative estimate for mixed English/Chinese content.
	CharsPerToken = 4

	// DefaultSafetyMargin is the fraction of context window kept as buffer.
	DefaultSafetyMargin = 0.05

	// MinSystemPromptTokens is the absolute floor for system prompt.
	MinSystemPromptTokens = 1000
)

// BudgetTier defines a priority range and its allocation weight.
type BudgetTier struct {
	MinPriority int
	MaxPriority int
	Weight      float64 // relative weight within total budget
	Guaranteed  bool    // if true, always gets MinTokens even when tight
}

// DefaultTiers defines the standard tier configuration.
var DefaultTiers = []BudgetTier{
	{MinPriority: 1, MaxPriority: 9, Weight: 0.10, Guaranteed: true},   // Identity
	{MinPriority: 10, MaxPriority: 19, Weight: 0.15, Guaranteed: true}, // Rules
	{MinPriority: 20, MaxPriority: 29, Weight: 0.45, Guaranteed: false}, // Tools (largest)
	{MinPriority: 30, MaxPriority: 49, Weight: 0.25, Guaranteed: false}, // Context
	{MinPriority: 90, MaxPriority: 99, Weight: 0.05, Guaranteed: false}, // Epilogue
}

// EstimateTokens returns an approximate token count for a string.
// For production use, replace with a proper tokenizer (tiktoken/etc).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// Simple estimation: count runes for better CJK handling
	runes := []rune(s)
	return len(runes) / CharsPerToken
}

// ComputeSystemPromptBudget calculates the token budget available
// for the system prompt given current conversation state.
func ComputeSystemPromptBudget(
	contextWindow int,
	conversationTokens int,
	maxOutputTokens int,
	safetyMargin float64,
) int {
	if safetyMargin <= 0 {
		safetyMargin = DefaultSafetyMargin
	}

	available := contextWindow - conversationTokens - maxOutputTokens
	margin := int(float64(contextWindow) * safetyMargin)
	budget := available - margin

	if budget < 0 {
		budget = 0
	}

	return budget
}

// BudgetAllocator distributes token budget across sections.
type BudgetAllocator struct {
	tiers []BudgetTier
}

// NewBudgetAllocator creates a budget allocator with default tiers.
func NewBudgetAllocator() *BudgetAllocator {
	return &BudgetAllocator{
		tiers: DefaultTiers,
	}
}

// Allocate distributes budget across sections using tier-based allocation.
// Returns a map of section name to allocated token budget.
func (ba *BudgetAllocator) Allocate(sections []Section, totalBudget int) map[string]int {
	allocation := make(map[string]int)

	if totalBudget < MinSystemPromptTokens {
		// Emergency mode: only allocate to guaranteed tiers
		return ba.allocateEmergency(sections, totalBudget)
	}

	// Group sections by tier
	tierSections := make(map[int][]Section)
	for _, s := range sections {
		priority := s.Priority()
		tierIdx := ba.findTierIndex(priority)
		if tierIdx >= 0 {
			tierSections[tierIdx] = append(tierSections[tierIdx], s)
		}
	}

	// Allocate budget to each tier
	remainingBudget := totalBudget
	for tierIdx, tier := range ba.tiers {
		secs := tierSections[tierIdx]
		if len(secs) == 0 {
			continue
		}

		tierBudget := int(float64(totalBudget) * tier.Weight)
		if tierBudget > remainingBudget {
			tierBudget = remainingBudget
		}

		// Allocate within tier
		tierAlloc := ba.allocateWithinTier(secs, tierBudget)
		for name, budget := range tierAlloc {
			allocation[name] = budget
		}

		remainingBudget -= tierBudget
		if remainingBudget <= 0 {
			break
		}
	}

	return allocation
}

// allocateEmergency handles extremely tight budgets by only allocating
// to guaranteed tiers (Identity + Rules).
func (ba *BudgetAllocator) allocateEmergency(sections []Section, totalBudget int) map[string]int {
	allocation := make(map[string]int)

	var guaranteedSections []Section
	for _, s := range sections {
		tierIdx := ba.findTierIndex(s.Priority())
		if tierIdx >= 0 && ba.tiers[tierIdx].Guaranteed {
			guaranteedSections = append(guaranteedSections, s)
		}
	}

	if len(guaranteedSections) == 0 {
		return allocation
	}

	// Distribute equally among guaranteed sections
	perSection := totalBudget / len(guaranteedSections)
	for _, s := range guaranteedSections {
		if perSection >= s.MinTokens() {
			allocation[s.Name()] = perSection
		}
	}

	return allocation
}

// allocateWithinTier distributes a tier's budget among its sections.
func (ba *BudgetAllocator) allocateWithinTier(sections []Section, tierBudget int) map[string]int {
	allocation := make(map[string]int)

	if len(sections) == 0 {
		return allocation
	}

	// Step 1: Grant MinTokens to each section
	minTotal := 0
	for _, s := range sections {
		minTotal += s.MinTokens()
	}

	if minTotal > tierBudget {
		// Not enough budget for all MinTokens - skip sections that can't fit
		remaining := tierBudget
		for _, s := range sections {
			if s.MinTokens() <= remaining {
				allocation[s.Name()] = s.MinTokens()
				remaining -= s.MinTokens()
			}
		}
		return allocation
	}

	// Step 2: Grant MinTokens to all
	for _, s := range sections {
		allocation[s.Name()] = s.MinTokens()
	}

	// Step 3: Distribute remaining budget equally
	remaining := tierBudget - minTotal
	if remaining > 0 {
		perSection := remaining / len(sections)
		for _, s := range sections {
			allocation[s.Name()] += perSection
		}
	}

	return allocation
}

// findTierIndex returns the tier index for a given priority, or -1 if not found.
func (ba *BudgetAllocator) findTierIndex(priority int) int {
	for i, tier := range ba.tiers {
		if priority >= tier.MinPriority && priority <= tier.MaxPriority {
			return i
		}
	}
	return -1
}
