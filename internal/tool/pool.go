package tool

import (
	"sort"

	"harnessclaw-go/internal/provider"
)

// ToolPool is the immutable, assembled set of tools for a query.
// It encapsulates the assembly pipeline from src/tools.ts:
//  1. Collect enabled built-in tools from Registry
//  2. Filter by deny rules
//  3. Merge with MCP tools
//  4. Sort for prompt-cache stability (built-in alphabetical prefix, MCP appended)
//  5. Deduplicate (built-in wins on name conflict)
//
// After construction, a ToolPool is read-only and safe for concurrent use.
type ToolPool struct {
	builtIn []Tool
	mcp     []Tool
	merged  []Tool            // computed once at construction
	byName  map[string]Tool   // name → tool (includes aliases)
}

// NewToolPool assembles a tool pool from built-in registry tools and MCP tools.
// denyRules is a list of tool names to exclude.
func NewToolPool(registry *Registry, mcpTools []Tool, denyRules []string) *ToolPool {
	tp := &ToolPool{
		byName: make(map[string]Tool),
	}

	denySet := make(map[string]bool, len(denyRules))
	for _, name := range denyRules {
		denySet[name] = true
	}

	// Step 1: Collect enabled built-in tools, filter by deny rules.
	for _, t := range registry.EnabledTools() {
		if denySet[t.Name()] {
			continue
		}
		tp.builtIn = append(tp.builtIn, t)
	}

	// Step 2: Collect enabled MCP tools, filter by deny rules.
	for _, t := range mcpTools {
		if !t.IsEnabled() || denySet[t.Name()] {
			continue
		}
		tp.mcp = append(tp.mcp, t)
	}

	// Step 3: Sort each group alphabetically by name for prompt-cache stability.
	sort.Slice(tp.builtIn, func(i, j int) bool {
		return tp.builtIn[i].Name() < tp.builtIn[j].Name()
	})
	sort.Slice(tp.mcp, func(i, j int) bool {
		return tp.mcp[i].Name() < tp.mcp[j].Name()
	})

	// Step 4: Merge — built-in as contiguous prefix, MCP appended.
	seen := make(map[string]bool)
	tp.merged = make([]Tool, 0, len(tp.builtIn)+len(tp.mcp))

	for _, t := range tp.builtIn {
		if !seen[t.Name()] {
			seen[t.Name()] = true
			tp.merged = append(tp.merged, t)
		}
	}
	for _, t := range tp.mcp {
		if !seen[t.Name()] {
			seen[t.Name()] = true
			tp.merged = append(tp.merged, t)
		}
	}

	// Step 5: Build byName map (includes aliases).
	for _, t := range tp.merged {
		tp.byName[t.Name()] = t
		if at, ok := t.(AliasedTool); ok {
			for _, alias := range at.Aliases() {
				if _, exists := tp.byName[alias]; !exists {
					tp.byName[alias] = t
				}
			}
		}
	}

	return tp
}

// Get looks up a tool by name or alias. Returns nil if not found.
func (tp *ToolPool) Get(name string) Tool {
	return tp.byName[name]
}

// All returns the assembled tool list in prompt-cache-stable order.
// The returned slice must not be modified by the caller.
func (tp *ToolPool) All() []Tool {
	return tp.merged
}

// Schemas returns provider.ToolSchema for the assembled pool.
func (tp *ToolPool) Schemas() []provider.ToolSchema {
	schemas := make([]provider.ToolSchema, len(tp.merged))
	for i, t := range tp.merged {
		schemas[i] = provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		}
	}
	return schemas
}

// Names returns the names of all tools in the pool.
func (tp *ToolPool) Names() []string {
	names := make([]string, len(tp.merged))
	for i, t := range tp.merged {
		names[i] = t.Name()
	}
	return names
}

// FilteredFor returns a sub-pool containing only tools allowed for the
// given agent type. See restrictions.go for the filtering logic.
func (tp *ToolPool) FilteredFor(agentType AgentType) *ToolPool {
	filtered := FilterToolsForAgent(tp.merged, agentType)

	sub := &ToolPool{
		merged: filtered,
		byName: make(map[string]Tool, len(filtered)),
	}
	for _, t := range filtered {
		sub.byName[t.Name()] = t
		if at, ok := t.(AliasedTool); ok {
			for _, alias := range at.Aliases() {
				if _, exists := sub.byName[alias]; !exists {
					sub.byName[alias] = t
				}
			}
		}
	}
	return sub
}

// FilterByNames returns a sub-pool containing only tools whose names are in
// the whitelist. If whitelist is nil or empty, returns the pool unchanged.
func (tp *ToolPool) FilterByNames(whitelist []string) *ToolPool {
	if len(whitelist) == 0 {
		return tp
	}
	allowed := make(map[string]bool, len(whitelist))
	for _, name := range whitelist {
		allowed[name] = true
	}
	var filtered []Tool
	for _, t := range tp.merged {
		if allowed[t.Name()] {
			filtered = append(filtered, t)
		}
	}
	sub := &ToolPool{
		merged: filtered,
		byName: make(map[string]Tool, len(filtered)),
	}
	for _, t := range filtered {
		sub.byName[t.Name()] = t
		if at, ok := t.(AliasedTool); ok {
			for _, alias := range at.Aliases() {
				if _, exists := sub.byName[alias]; !exists {
					sub.byName[alias] = t
				}
			}
		}
	}
	return sub
}

// Size returns the number of tools in the pool.
func (tp *ToolPool) Size() int {
	return len(tp.merged)
}
