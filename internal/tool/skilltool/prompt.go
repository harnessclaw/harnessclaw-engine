package skilltool

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/command"
)

// Budget constants — mirrors src/tools/SkillTool/prompt.ts.
const (
	// SkillBudgetContextPercent is the fraction of the context window
	// allocated to the skill listing (1%).
	SkillBudgetContextPercent = 0.01

	// CharsPerToken is the approximate chars-per-token conversion factor.
	CharsPerToken = 4

	// DefaultCharBudget is the fallback budget when context window is unknown.
	// Equivalent to 1% of 200k tokens × 4 chars/token.
	DefaultCharBudget = 8000

	// MaxListingDescChars is the hard cap on a single skill's description
	// in the listing. The listing is for discovery only — the Skill tool
	// loads full content on invoke.
	MaxListingDescChars = 250
)

// getCharBudget computes the character budget for the skill listing.
// Mirrors TS getCharBudget().
func getCharBudget(contextWindowTokens int) int {
	if contextWindowTokens > 0 {
		return int(float64(contextWindowTokens) * CharsPerToken * SkillBudgetContextPercent)
	}
	return DefaultCharBudget
}

// getCommandDescription builds a description string from description + whenToUse,
// truncated to MaxListingDescChars. Mirrors TS getCommandDescription().
func getCommandDescription(pc *command.PromptCommand) string {
	desc := pc.Description
	if pc.WhenToUse != "" {
		desc = desc + " - " + pc.WhenToUse
	}
	if len(desc) > MaxListingDescChars {
		desc = desc[:MaxListingDescChars-1] + "…"
	}
	return desc
}

// FormatCommandsWithinBudget generates the skill listing within a character budget.
// The budget is approximately 1% of the context window (in characters).
//
// This mirrors formatCommandsWithinBudget from src/tools/SkillTool/prompt.ts.
//
// Algorithm:
//  1. Try full descriptions for all commands.
//  2. If over budget, truncate descriptions proportionally.
//  3. If still over budget, fall back to names-only for non-bundled.
func FormatCommandsWithinBudget(commands []*command.PromptCommand, contextWindowTokens int) string {
	if len(commands) == 0 {
		return ""
	}

	budget := getCharBudget(contextWindowTokens)

	// Try full descriptions first.
	fullEntries := make([]string, len(commands))
	fullTotal := 0
	for i, pc := range commands {
		fullEntries[i] = fmt.Sprintf("- %s: %s", pc.Name, getCommandDescription(pc))
		fullTotal += len(fullEntries[i])
	}
	fullTotal += len(commands) - 1 // newline separators

	if fullTotal <= budget {
		return strings.Join(fullEntries, "\n")
	}

	// Over budget — compute per-entry description limit.
	// Name overhead: "- <name>: " per entry + newlines between entries.
	nameOverhead := 0
	for _, pc := range commands {
		nameOverhead += len(pc.Name) + 4 // "- " + ": "
	}
	nameOverhead += len(commands) - 1 // newlines

	availableForDescs := budget - nameOverhead
	if availableForDescs < 0 {
		availableForDescs = 0
	}
	maxDescLen := availableForDescs / len(commands)

	const minDescLength = 20
	if maxDescLen < minDescLength {
		// Extreme case: names only.
		entries := make([]string, len(commands))
		for i, pc := range commands {
			entries[i] = fmt.Sprintf("- %s", pc.Name)
		}
		return strings.Join(entries, "\n")
	}

	// Truncate descriptions to fit within budget.
	entries := make([]string, len(commands))
	for i, pc := range commands {
		desc := getCommandDescription(pc)
		if len(desc) > maxDescLen {
			desc = desc[:maxDescLen-1] + "…"
		}
		entries[i] = fmt.Sprintf("- %s: %s", pc.Name, desc)
	}
	return strings.Join(entries, "\n")
}
