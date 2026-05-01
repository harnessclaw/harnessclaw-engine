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

// Schemas returns provider.ToolSchema for the assembled pool. Every tool's
// input schema is decorated with a required `intent` field so the model is
// forced to declare what each tool call is for. The decoration happens here
// (not on the Tool itself) so individual tool implementations stay clean and
// the constraint is uniformly enforced — see ToolExecutor.executeSingle for
// the matching strip-and-emit step on the execution side.
func (tp *ToolPool) Schemas() []provider.ToolSchema {
	schemas := make([]provider.ToolSchema, len(tp.merged))
	for i, t := range tp.merged {
		schemas[i] = provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: WithIntentField(t.InputSchema()),
		}
	}
	return schemas
}

// IntentFieldName is the schema property the framework injects on every
// tool's input schema. Exported so executor and tests share the constant.
const IntentFieldName = "intent"

// IntentFieldDescription is the model-facing instruction. Worded so the model
// understands WHY the field exists and WHAT to put there — the goal is a
// short, user-readable progress sentence rather than the literal task text.
const IntentFieldDescription = "用一句话告诉用户你这次调用要做什么（例如：「正在搜索 vLLM 论文」「读取 main.go 找入口函数」）。这条文本会实时显示给用户作为进度提示，必须填写。"

// WithIntentField returns a deep-enough copy of the tool input schema with
// the framework-required `intent` field added to properties and required.
// If the original schema already declares `intent`, returns it unchanged
// (a tool that wants its own intent semantics wins). Tolerates missing or
// non-object schemas — invalid schemas pass through and the model will
// fail validation downstream, which is the correct signal.
func WithIntentField(orig map[string]any) map[string]any {
	if orig == nil {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				IntentFieldName: map[string]any{
					"type":        "string",
					"description": IntentFieldDescription,
				},
			},
			"required": []any{IntentFieldName},
		}
	}

	// Copy top level so we don't mutate the tool's own schema.
	out := make(map[string]any, len(orig)+1)
	for k, v := range orig {
		out[k] = v
	}

	// Copy properties and add intent (skip if tool already defines it).
	props, _ := out["properties"].(map[string]any)
	newProps := make(map[string]any, len(props)+1)
	for k, v := range props {
		newProps[k] = v
	}
	if _, exists := newProps[IntentFieldName]; !exists {
		newProps[IntentFieldName] = map[string]any{
			"type":        "string",
			"description": IntentFieldDescription,
		}
	}
	out["properties"] = newProps

	// Copy required and ensure intent is in it (idempotent).
	required := toStringSlice(out["required"])
	hasIntent := false
	for _, r := range required {
		if r == IntentFieldName {
			hasIntent = true
			break
		}
	}
	if !hasIntent {
		required = append(required, IntentFieldName)
	}
	// Re-emit as []any to match what most JSON-schema producers use.
	asAny := make([]any, len(required))
	for i, s := range required {
		asAny[i] = s
	}
	out["required"] = asAny

	return out
}

// toStringSlice tolerates both []string and []any forms of the JSON-schema
// `required` array — the input came from arbitrary tool authors, not from
// JSON unmarshalling, so the type isn't fixed.
func toStringSlice(v any) []string {
	switch r := v.(type) {
	case []string:
		out := make([]string, len(r))
		copy(out, r)
		return out
	case []any:
		out := make([]string, 0, len(r))
		for _, x := range r {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
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
