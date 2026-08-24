package common

import (
	"harnessclaw-go/internal/tools"
)

// dispatchToolNames lists tools that fan out to sub-agents. They are
// stripped from the pool for strict leaf agents (TierSubAgent) so a
// sub-agent cannot recursively spawn deeper.
//
// "dispatch" 是 emma 用的 L1→L3 派发工具（原名 "scheduler"），
// "freelance" 是 agenttool 的工具名（保留），
// "task" 是历史 fallback 名（保留以防外部 schema 引用）。
var dispatchToolNames = []string{"freelance", "dispatch", "task"}

// BuildToolPool produces the tool pool for a sub-agent.
//
// Key precedence rule: **AllowedTools whitelist is authoritative when
// non-empty, and bypasses the AgentType blacklist**. This matches the
// design intent recorded in:
//   - tool/restrictions.go:93 → "freelance: bypassed by AgentDefinition.
//     AllowedTools whitelist (scheduler)"
//   - agent/definition.go scheduler block → "tool filter pipeline treats
//     AllowedTools as authoritative — bypasses the AgentType blacklist
//     (which would otherwise block task for sync sub-agents)"
//
// Without this bypass, AgentTypeSync's AllAgentDisallowed strips
// `freelance` and `scheduler` before any whitelist can re-admit them,
// so L2 react LLM never sees the dispatch tools its principles tell it
// to use (regression observed when L2 in-module loop landed: LLM kept
// hallucinating `<freelance>...` markup because the schema said the
// tool was absent).
//
// Behavior:
//  1. start from the full registry
//  2. whitelist supplied → keep ONLY whitelisted tools, skip blacklist
//     no whitelist        → apply AgentType blacklist
//  3. optionally strip dispatch tools for strict leaves (applies after
//     either branch so a whitelist that mistakenly named a dispatch
//     tool still gets clamped)
//  4. MCP tools, when supplied via WithMCPTools, join the pool as its
//     mcp group — appended after the built-in block so the built-in
//     prefix stays byte-stable for prompt caching. They are subject to
//     the same whitelist/blacklist passes as built-ins, so a definition
//     admits them with an "mcp__*" entry in AllowedTools (see
//     tool.ToolPool.FilterByNames).
func BuildToolPool(registry *tool.Registry, allowed []string, agentType tool.AgentType, stripDispatch bool, opts ...PoolOption) *tool.ToolPool {
	var o poolOptions
	for _, fn := range opts {
		fn(&o)
	}

	pool := tool.NewToolPool(registry, o.mcpTools, nil)

	if len(allowed) > 0 {
		pool = pool.FilterByNames(allowed)
	} else {
		pool = pool.FilteredFor(agentType)
	}

	if stripDispatch {
		pool = pool.WithoutNames(dispatchToolNames)
	}

	return pool
}

// poolOptions carries the optional inputs to BuildToolPool.
type poolOptions struct {
	mcpTools []tool.Tool
}

// PoolOption configures BuildToolPool.
//
// Options rather than extra parameters: BuildToolPool has two production
// call sites and a dozen in tests, and MCP is additive — an option keeps
// every existing caller compiling unchanged while letting the two paths
// that can reach a Manager opt in.
type PoolOption func(*poolOptions)

// WithMCPTools supplies the MCP-backed tools for this pool. Pass the
// slice from mcptool.NewTools. Nil or empty is a no-op, which is the
// normal case for a user with no MCP servers configured.
func WithMCPTools(tools []tool.Tool) PoolOption {
	return func(o *poolOptions) { o.mcpTools = tools }
}
