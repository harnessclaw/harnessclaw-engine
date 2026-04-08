package tool

// AgentType classifies the agent context for tool filtering.
// Mirrors the restriction sets from src/constants/tools.ts.
type AgentType string

const (
	// AgentTypeSync is a synchronous sub-agent (runs inline in parent context).
	AgentTypeSync AgentType = "sync"
	// AgentTypeAsync is an asynchronous agent (runs in background).
	AgentTypeAsync AgentType = "async"
	// AgentTypeTeammate is an in-process teammate agent.
	AgentTypeTeammate AgentType = "teammate"
	// AgentTypeCoordinator is the coordinator mode agent.
	AgentTypeCoordinator AgentType = "coordinator"
	// AgentTypeCustom is a custom agent from agent definitions.
	AgentTypeCustom AgentType = "custom"
)

// ---------------------------------------------------------------------------
// Tool restriction sets — ported from src/constants/tools.ts
//
// These sets control which tools are available to different agent types.
// Blacklists block specific tools; whitelists define the only tools allowed.
// ---------------------------------------------------------------------------

// AllAgentDisallowed lists tools blocked for ALL sub-agents.
// Reason for each entry:
//   - TaskOutput: prevents recursion
//   - ExitPlanMode: plan mode is a main thread abstraction
//   - EnterPlanMode: plan mode is a main thread abstraction
//   - AskUserQuestion: sub-agents cannot prompt the user directly
//   - TaskStop: requires access to main thread task state
//   - Agent: blocked to prevent recursion (may be conditionally enabled)
var AllAgentDisallowed = map[string]bool{
	"TaskOutput":      true,
	"ExitPlanMode":    true,
	"EnterPlanMode":   true,
	"AskUserQuestion": true,
	"TaskStop":        true,
	"Agent":           true, // conditionally enabled for internal users in TS via USER_TYPE=ant
}

// CustomAgentDisallowed is the blacklist for custom agents.
// Currently identical to AllAgentDisallowed; reserved for future restrictions.
var CustomAgentDisallowed = copySet(AllAgentDisallowed)

// AsyncAgentAllowed is the whitelist of tools available to async agents.
// Only tools in this list are permitted for async execution.
var AsyncAgentAllowed = map[string]bool{
	"Read":            true,
	"WebSearch":       true,
	"TodoWrite":       true,
	"Grep":            true,
	"WebFetch":        true,
	"Glob":            true,
	"Bash":            true,
	"Edit":            true,
	"Write":           true,
	"NotebookEdit":    true,
	"Skill":           true,
	"SyntheticOutput": true,
	"ToolSearch":      true,
	"EnterWorktree":   true,
	"ExitWorktree":    true,
}

// InProcessTeammateExtra lists additional tools for in-process teammates,
// beyond the AsyncAgentAllowed set.
var InProcessTeammateExtra = map[string]bool{
	"TaskCreate":  true,
	"TaskGet":     true,
	"TaskList":    true,
	"TaskUpdate":  true,
	"SendMessage": true,
	// CronCreate, CronDelete, CronList conditionally added at runtime
}

// CoordinatorAllowed is the whitelist of tools for coordinator mode.
// The coordinator only dispatches work to agents; it does not execute tools directly.
var CoordinatorAllowed = map[string]bool{
	"Agent":           true,
	"TaskStop":        true,
	"SendMessage":     true,
	"SyntheticOutput": true,
}

// FilterToolsForAgent applies the appropriate restriction strategy for the given agent type.
//
// Strategy by agent type:
//   - Sync: remove AllAgentDisallowed tools (blacklist)
//   - Async: keep only AsyncAgentAllowed tools (whitelist)
//   - Teammate: keep AsyncAgentAllowed + InProcessTeammateExtra (whitelist)
//   - Coordinator: keep only CoordinatorAllowed tools (whitelist)
//   - Custom: remove CustomAgentDisallowed tools (blacklist)
func FilterToolsForAgent(tools []Tool, agentType AgentType) []Tool {
	switch agentType {
	case AgentTypeSync:
		return filterByBlacklist(tools, AllAgentDisallowed)
	case AgentTypeAsync:
		return filterByWhitelist(tools, AsyncAgentAllowed)
	case AgentTypeTeammate:
		combined := copySet(AsyncAgentAllowed)
		for k := range InProcessTeammateExtra {
			combined[k] = true
		}
		return filterByWhitelist(tools, combined)
	case AgentTypeCoordinator:
		return filterByWhitelist(tools, CoordinatorAllowed)
	case AgentTypeCustom:
		return filterByBlacklist(tools, CustomAgentDisallowed)
	default:
		// Unknown agent type — apply the strictest restriction (sync blacklist).
		return filterByBlacklist(tools, AllAgentDisallowed)
	}
}

// filterByBlacklist removes tools whose Name is in the blacklist.
func filterByBlacklist(tools []Tool, blacklist map[string]bool) []Tool {
	result := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if !blacklist[t.Name()] {
			result = append(result, t)
		}
	}
	return result
}

// filterByWhitelist keeps only tools whose Name is in the whitelist.
func filterByWhitelist(tools []Tool, whitelist map[string]bool) []Tool {
	result := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if whitelist[t.Name()] {
			result = append(result, t)
		}
	}
	return result
}

// copySet returns a shallow copy of a string→bool map.
func copySet(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
