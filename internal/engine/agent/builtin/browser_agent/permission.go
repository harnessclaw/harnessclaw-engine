package browser_agent

import "harnessclaw-go/internal/engine/agent/definition"

// ExpandApprovedTools inherits a parent approval for the browser_agent entry
// tool into the Browser Agent's internal tool palette.
func ExpandApprovedTools(parentApproved []string, def *definition.AgentDefinition) []string {
	if !containsTool(parentApproved, ToolName) {
		return parentApproved
	}
	internalTools := def.MaybeAugmentForSubAgent()
	approved := make([]string, 0, len(parentApproved)+len(internalTools))
	seen := make(map[string]bool, len(parentApproved)+len(internalTools))
	for _, toolName := range parentApproved {
		if seen[toolName] {
			continue
		}
		seen[toolName] = true
		approved = append(approved, toolName)
	}
	for _, toolName := range internalTools {
		if seen[toolName] {
			continue
		}
		seen[toolName] = true
		approved = append(approved, toolName)
	}
	return approved
}

func containsTool(tools []string, want string) bool {
	for _, toolName := range tools {
		if toolName == want {
			return true
		}
	}
	return false
}
