package common

import (
	"harnessclaw-go/internal/tool"
)

// dispatchToolNames lists tools that fan out to sub-agents. They are
// stripped from the pool for strict leaf agents (TierSubAgent) so a
// sub-agent cannot recursively spawn deeper.
var dispatchToolNames = []string{"freelance", "scheduler", "task"}

// BuildToolPool applies the standard 3-layer filter:
//  1. Start from the full registry.
//  2. AgentType blacklist (e.g. sync agents get task tool stripped).
//  3. AllowedTools whitelist (caller-specified set).
//  4. Optionally strip dispatch tools for strict leaves.
func BuildToolPool(registry *tool.Registry, allowed []string, agentType tool.AgentType, stripDispatch bool) *tool.ToolPool {
	pool := tool.NewToolPool(registry, nil, nil)

	// Step 2: AgentType blacklist applied via FilteredFor
	pool = pool.FilteredFor(agentType)

	// Step 3: AllowedTools whitelist (only when non-empty)
	if len(allowed) > 0 {
		pool = pool.FilterByNames(allowed)
	}

	// Step 4: strip dispatch tools for TierSubAgent
	if stripDispatch {
		pool = pool.WithoutNames(dispatchToolNames)
	}

	return pool
}
