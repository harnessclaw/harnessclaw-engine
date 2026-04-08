package skilltool

import "harnessclaw-go/internal/command"

// HasOnlySafeProperties checks if a PromptCommand only has safe properties set.
// Commands with only safe properties do not require additional permission to execute.
//
// This mirrors SAFE_SKILL_PROPERTIES from src/tools/SkillTool/SkillTool.ts.
// A skill is "safe" if it doesn't override model, effort, allowedTools, or hooks.
func HasOnlySafeProperties(pc *command.PromptCommand) bool {
	// Unsafe if it overrides model execution context.
	if pc.Model != "" {
		return false
	}
	if pc.Effort != "" {
		return false
	}
	if len(pc.AllowedTools) > 0 {
		return false
	}
	// Forked execution context requires additional permission.
	if pc.Context == "fork" {
		return false
	}
	if pc.Agent != "" {
		return false
	}
	// All remaining properties (Name, Description, Aliases, WhenToUse, etc.) are safe.
	return true
}
