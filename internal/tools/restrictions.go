package tool

// SafetyLevel classifies how risky a tool call is. Tools opt in via the
// optional SafetyLeveler interface; tools that don't implement it default
// to SafetyCaution — a deliberate "fail-safe" choice that forces authors
// to explicitly downgrade rather than accidentally elevate.
//
// Three buckets cover what matters operationally:
//   - SafetySafe:      pure read / lookup, no side effect (Read, Grep, Glob,
//                      web_fetch, web_search, ArtifactRead).
//   - SafetyCaution:   local mutation that's easily reversible
//                      (Write, Edit, ArtifactWrite, FileWrite).
//   - SafetyDangerous: shell/network with external side effects
//                      (Bash, send_email, make_payment when added).
//
// The L3 sub-agent driver strips dangerous tools from the pool unless
// they're explicitly listed in AgentDefinition.AllowedTools — protecting
// against accidental exposure when a worker's whitelist is empty.
type SafetyLevel string

const (
	SafetySafe      SafetyLevel = "safe"
	SafetyCaution   SafetyLevel = "caution"
	SafetyDangerous SafetyLevel = "dangerous"
)

// SafetyLeveler is implemented by tools that wish to declare their own
// safety level. Tools that don't implement it inherit SafetyCaution from
// EffectiveSafetyLevel — fail-safe.
type SafetyLeveler interface {
	SafetyLevel() SafetyLevel
}

// EffectiveSafetyLevel returns the tool's declared SafetyLevel, falling
// back to SafetyCaution for tools that don't opt in. A central helper so
// the policy lives in one place — caller doesn't have to know whether
// the tool implements the interface.
func EffectiveSafetyLevel(t Tool) SafetyLevel {
	if sl, ok := t.(SafetyLeveler); ok {
		lvl := sl.SafetyLevel()
		if lvl != "" {
			return lvl
		}
	}
	return SafetyCaution
}

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

// AllAgentDisallowed lists tools blocked for ALL sub-agents by default.
//
// Important: AgentDefinition.AllowedTools acts as an EXPLICIT WHITELIST
// that bypasses this blacklist. scheduler (L2) declares
// AllowedTools=["task", ...], which lets it use the task tool to dispatch
// L3 even though task is in this list. See subagent.go's filter pipeline.
//
// Reason for each entry:
//   - TaskOutput: prevents recursion
//   - ExitPlanMode: plan mode is a main thread abstraction
//   - EnterPlanMode: plan mode is a main thread abstraction
//   - ask_user_question: only emma (L1) is allowed to prompt the user
//   - TaskStop: requires access to main thread task state
//   - task: prevents arbitrary recursion
//   - dispatch: emma 的入口 dispatch 工具 —— L3 sub-agent 不能回过头来递归调度
var AllAgentDisallowed = map[string]bool{
	"TaskOutput":        true,
	"ExitPlanMode":      true,
	"EnterPlanMode":     true,
	"ask_user_question": true,
	"TaskStop":          true,
	"freelance":         true, // bypassed by AgentDefinition.AllowedTools whitelist
	"dispatch":          true, // emma 入口；L3 cannot recurse upward
	"orchestrate":       true, // L2 plan-mode tool; surfacing it to L3 leaves them hallucinating calls that fail "intent is required" immediately
}

// CustomAgentDisallowed is the blacklist for custom agents.
// Currently identical to AllAgentDisallowed; reserved for future restrictions.
var CustomAgentDisallowed = copySet(AllAgentDisallowed)

// AsyncAgentAllowed is the whitelist of tools available to async agents.
// Only tools in this list are permitted for async execution.
var AsyncAgentAllowed = map[string]bool{
	"read":            true,
	"web_search":       true,
	"TodoWrite":       true,
	"grep":            true,
	"web_fetch":        true,
	"glob":            true,
	"bash":            true,
	"edit":            true,
	"write":           true,
	"NotebookEdit":    true,
	"skill":           true,
	"SyntheticOutput": true,
	"ToolSearch":      true,
	"EnterWorktree":   true,
	"ExitWorktree":    true,
}

// InProcessTeammateExtra lists additional tools for in-process teammates,
// beyond the AsyncAgentAllowed set.
var InProcessTeammateExtra = map[string]bool{
	"task_create":  true,
	"task_get":     true,
	"task_list":    true,
	"task_update":  true,
	"send_message": true,
	// CronCreate, CronDelete, CronList conditionally added at runtime
}

// CoordinatorAllowed is the whitelist of tools for coordinator mode.
// The coordinator only dispatches work to agents; it does not execute tools directly.
var CoordinatorAllowed = map[string]bool{
	"freelance":       true, // L2→L3 dispatch (mirrors how L1 uses "scheduler")
	"Agent":           true,
	"TaskStop":        true,
	"send_message":    true,
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
