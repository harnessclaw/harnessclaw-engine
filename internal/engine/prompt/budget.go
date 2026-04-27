package prompt

const (
	// CharsPerTokenCJK is the approximate chars-per-token for CJK-dominant content.
	// Chinese characters typically tokenize to ~1.5-2 chars per token.
	CharsPerTokenCJK = 2

	// CharsPerTokenLatin is the approximate chars-per-token for Latin-dominant content.
	CharsPerTokenLatin = 4

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
// Used as fallback when sections don't implement BudgetAwareSection.
var DefaultTiers = []BudgetTier{
	{MinPriority: 1, MaxPriority: 9, Weight: 0.10, Guaranteed: true},   // Identity
	{MinPriority: 10, MaxPriority: 19, Weight: 0.15, Guaranteed: true}, // Rules
	{MinPriority: 20, MaxPriority: 29, Weight: 0.45, Guaranteed: false}, // Tools (largest)
	{MinPriority: 30, MaxPriority: 49, Weight: 0.25, Guaranteed: false}, // Context
	{MinPriority: 90, MaxPriority: 99, Weight: 0.05, Guaranteed: false}, // Epilogue
}

// EstimateTokens returns an approximate token count for a string.
// Detects CJK vs Latin content ratio and uses the appropriate conversion factor.
// For production use, replace with a proper tokenizer (tiktoken/etc).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := []rune(s)
	total := len(runes)
	if total == 0 {
		return 0
	}

	// Count CJK characters to determine dominant language
	cjkCount := 0
	for _, r := range runes {
		if isCJK(r) {
			cjkCount++
		}
	}

	cjkRatio := float64(cjkCount) / float64(total)

	// Blend the conversion factor based on CJK ratio
	// Pure CJK → 2 chars/token, Pure Latin → 4 chars/token
	charsPerToken := float64(CharsPerTokenLatin) - float64(CharsPerTokenLatin-CharsPerTokenCJK)*cjkRatio
	if charsPerToken < float64(CharsPerTokenCJK) {
		charsPerToken = float64(CharsPerTokenCJK)
	}

	return int(float64(total) / charsPerToken)
}

// isCJK returns true if the rune is in a CJK Unicode block.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Unified Ideographs Extension A
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth and Fullwidth Forms
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

// Allocate distributes budget using a hybrid strategy:
//  1. If ANY section implements BudgetAwareSection, use demand-driven allocation
//  2. Otherwise, fall back to static tier-based allocation
//
// Demand-driven allocation:
//   - Each BudgetAwareSection declares its ideal tokens
//   - Non-aware sections get a tier-based estimate
//   - If total demand <= budget, everyone gets what they asked for
//   - If total demand > budget, trim from lowest-priority sections first
func (ba *BudgetAllocator) Allocate(sections []Section, totalBudget int) map[string]int {
	if totalBudget < MinSystemPromptTokens {
		return ba.allocateEmergency(sections, totalBudget)
	}

	// Check if any section is budget-aware
	hasBudgetAware := false
	for _, s := range sections {
		if _, ok := s.(BudgetAwareSection); ok {
			hasBudgetAware = true
			break
		}
	}

	if hasBudgetAware {
		return ba.allocateDynamic(sections, totalBudget, nil)
	}
	return ba.allocateStatic(sections, totalBudget)
}

// AllocateDynamic is the demand-driven allocation entry point.
// ctx may be nil if no BudgetAwareSection needs it.
func (ba *BudgetAllocator) AllocateDynamic(sections []Section, totalBudget int, ctx *PromptContext) map[string]int {
	if totalBudget < MinSystemPromptTokens {
		return ba.allocateEmergency(sections, totalBudget)
	}
	return ba.allocateDynamic(sections, totalBudget, ctx)
}

// allocateDynamic implements the demand-driven strategy.
func (ba *BudgetAllocator) allocateDynamic(sections []Section, totalBudget int, ctx *PromptContext) map[string]int {
	allocation := make(map[string]int)

	type demand struct {
		section  Section
		ideal    int
		priority int
	}

	demands := make([]demand, 0, len(sections))
	totalDemand := 0

	for _, s := range sections {
		ideal := 0
		if bas, ok := s.(BudgetAwareSection); ok && ctx != nil {
			ideal = bas.IdealTokens(ctx)
		}
		// If section doesn't declare ideal or returns 0, estimate from tier weight.
		if ideal == 0 {
			ideal = ba.estimateFromTier(s, totalBudget)
		}
		// Floor: at least MinTokens.
		if ideal < s.MinTokens() {
			ideal = s.MinTokens()
		}

		demands = append(demands, demand{
			section:  s,
			ideal:    ideal,
			priority: s.Priority(),
		})
		totalDemand += ideal
	}

	if totalDemand <= totalBudget {
		// Happy path: everyone gets what they asked for.
		// Distribute surplus proportionally.
		surplus := totalBudget - totalDemand
		for _, d := range demands {
			extra := 0
			if totalDemand > 0 {
				extra = int(float64(surplus) * float64(d.ideal) / float64(totalDemand))
			}
			allocation[d.section.Name()] = d.ideal + extra
		}
		return allocation
	}

	// Over budget: trim from lowest-priority sections first.
	// Sort by priority descending (highest priority number = lowest importance = trimmed first).
	// We use a greedy approach: give each section its MinTokens first,
	// then distribute remaining budget by priority (lower number = higher priority).

	// Step 1: Grant MinTokens to all.
	minTotal := 0
	for _, d := range demands {
		allocation[d.section.Name()] = d.section.MinTokens()
		minTotal += d.section.MinTokens()
	}

	if minTotal >= totalBudget {
		// Even MinTokens exceeds budget — keep what fits, skip the rest by priority.
		return ba.allocateEmergency(sections, totalBudget)
	}

	// Step 2: Distribute remaining budget to sections, prioritizing lower priority numbers.
	remaining := totalBudget - minTotal
	// Sort demands by priority ascending (most important first).
	sortedDemands := make([]demand, len(demands))
	copy(sortedDemands, demands)
	for i := 0; i < len(sortedDemands); i++ {
		for j := i + 1; j < len(sortedDemands); j++ {
			if sortedDemands[j].priority < sortedDemands[i].priority {
				sortedDemands[i], sortedDemands[j] = sortedDemands[j], sortedDemands[i]
			}
		}
	}

	for _, d := range sortedDemands {
		want := d.ideal - d.section.MinTokens() // additional tokens beyond MinTokens
		if want <= 0 {
			continue
		}
		if want > remaining {
			want = remaining
		}
		allocation[d.section.Name()] += want
		remaining -= want
		if remaining <= 0 {
			break
		}
	}

	return allocation
}

// estimateFromTier computes a fallback ideal budget for a section based on its tier weight.
func (ba *BudgetAllocator) estimateFromTier(s Section, totalBudget int) int {
	tierIdx := ba.findTierIndex(s.Priority())
	if tierIdx < 0 {
		return s.MinTokens()
	}
	return int(float64(totalBudget) * ba.tiers[tierIdx].Weight)
}

// allocateStatic is the original tier-based allocation (backward compatible).
func (ba *BudgetAllocator) allocateStatic(sections []Section, totalBudget int) map[string]int {
	allocation := make(map[string]int)

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
