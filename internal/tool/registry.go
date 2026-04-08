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
