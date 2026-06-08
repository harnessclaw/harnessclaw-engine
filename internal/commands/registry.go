package command

import (
	"sort"
	"strings"
	"sync"
)

// Registry manages command discovery and lookup with priority-based merge.
type Registry struct {
	mu       sync.RWMutex
	commands map[string]*Command // name → command (first-wins based on source priority)
	aliases  map[string]string   // alias → canonical name
}

// NewRegistry creates an empty command registry.
func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]*Command),
		aliases:  make(map[string]string),
	}
}

// Register adds a command. If a command with the same name already exists,
// the one with higher priority (lower Source value) wins.
func (r *Registry) Register(cmd *Command) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := cmd.GetName()
	if name == "" {
		return
	}

	base := cmd.GetBase()
	if base == nil {
		return
	}

	// Check if existing command has higher priority.
	if existing, ok := r.commands[name]; ok {
		existingBase := existing.GetBase()
		if existingBase != nil && existingBase.Source <= base.Source {
			return // existing has higher or equal priority
		}
	}

	r.commands[name] = cmd

	// Register aliases.
	if base.Aliases != nil {
		for _, alias := range base.Aliases {
			r.aliases[alias] = name
		}
	}
}

// LoadAll loads commands from multiple sources. Commands are registered
// with priority-based deduplication (first-wins for same priority).
func (r *Registry) LoadAll(commandSets ...[]Command) {
	for _, cmds := range commandSets {
		for i := range cmds {
			r.Register(&cmds[i])
		}
	}
}

// Get looks up a command by name or alias. Returns nil if not found.
func (r *Registry) Get(name string) *Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if cmd, ok := r.commands[name]; ok {
		return cmd
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.commands[canonical]
	}
	return nil
}

// FindCommand looks up by name or alias, case-insensitive.
func (r *Registry) FindCommand(name string) *Command {
	// Try exact match first.
	if cmd := r.Get(name); cmd != nil {
		return cmd
	}

	// Case-insensitive fallback.
	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(name)
	for k, cmd := range r.commands {
		if strings.ToLower(k) == lower {
			return cmd
		}
	}
	for alias, canonical := range r.aliases {
		if strings.ToLower(alias) == lower {
			return r.commands[canonical]
		}
	}
	return nil
}

// GetSkillToolCommands returns prompt commands callable by the model via SkillTool.
// Filters: type=prompt, !DisableModelInvocation, has description or whenToUse.
func (r *Registry) GetSkillToolCommands() []*PromptCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*PromptCommand
	for _, cmd := range r.commands {
		if cmd.Type != CommandTypePrompt || cmd.Prompt == nil {
			continue
		}
		pc := cmd.Prompt
		if pc.DisableModelInvocation {
			continue
		}
		if !pc.IsEnabled {
			continue
		}
		if pc.Description == "" && pc.WhenToUse == "" {
			continue
		}
		result = append(result, pc)
	}

	// Sort by name for deterministic output (prompt-cache stability).
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// All returns all commands in the registry.
func (r *Registry) All() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		result = append(result, cmd)
	}
	return result
}

// Names returns all registered command names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.commands))
	for name := range r.commands {
		names = append(names, name)
	}
	return names
}
