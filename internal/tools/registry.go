package tool

import (
	"fmt"
	"sort"
	"sync"

	"harnessclaw-go/internal/provider"
)

// Registry manages tool registration, discovery, and schema assembly.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	aliases map[string]string // alias → canonical name
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		aliases: make(map[string]string),
	}
}

// Register adds a tool. Returns an error if the name is already taken.
// If the tool implements AliasedTool, aliases are also registered.
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[t.Name()]; exists {
		return fmt.Errorf("tool already registered: %s", t.Name())
	}
	r.tools[t.Name()] = t

	// Register aliases if the tool provides them.
	if at, ok := t.(AliasedTool); ok {
		for _, alias := range at.Aliases() {
			r.aliases[alias] = t.Name()
		}
	}

	return nil
}

// Replace atomically swaps the registered tool for name with newTool.
// Both name and newTool.Name() must match (so callers can't accidentally
// rename via Replace). Returns an error if the name isn't registered or
// newTool is nil. If the existing tool exposed aliases (via AliasedTool),
// they're refreshed from newTool's aliases — old-only aliases are dropped
// and new-only aliases are added.
//
// Use case: runtime config update via the tools management API rebuilds
// a tool instance from new credentials, and we hot-swap it under the
// same registered name. ToolPool snapshots taken before Replace keep
// pointing at the old instance, so in-flight spawns finish on the old
// credentials — the next spawn picks up the new tool from registry.
func (r *Registry) Replace(name string, newTool Tool) error {
	if newTool == nil {
		return fmt.Errorf("tool.Registry.Replace: nil tool for %q", name)
	}
	if newTool.Name() != name {
		return fmt.Errorf("tool.Registry.Replace: name mismatch (registered %q, new %q)", name, newTool.Name())
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return fmt.Errorf("tool.Registry.Replace: %q not registered", name)
	}

	// Strip aliases pointing at the old tool, then re-register from newTool.
	for alias, canonical := range r.aliases {
		if canonical == name {
			delete(r.aliases, alias)
		}
	}
	r.tools[name] = newTool
	if at, ok := newTool.(AliasedTool); ok {
		for _, alias := range at.Aliases() {
			r.aliases[alias] = name
		}
	}
	return nil
}

// Get retrieves a tool by name or alias. Returns nil if not found.
func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if t, ok := r.tools[name]; ok {
		return t
	}
	// Fallback: check alias map.
	if canonical, ok := r.aliases[name]; ok {
		return r.tools[canonical]
	}
	return nil
}

// All returns all registered tools sorted by name (for prompt-cache stability).
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

// EnabledTools returns only enabled tools, sorted by name.
func (r *Registry) EnabledTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if t.IsEnabled() {
			result = append(result, t)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result
}

// Schemas returns tool schemas suitable for the LLM provider.
func (r *Registry) Schemas() []provider.ToolSchema {
	tools := r.All()
	schemas := make([]provider.ToolSchema, len(tools))
	for i, t := range tools {
		schemas[i] = provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		}
	}
	return schemas
}
