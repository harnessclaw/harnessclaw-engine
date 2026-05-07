package agent

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"harnessclaw-go/internal/event"
)

// Event topics for agent definition lifecycle.
const (
	TopicAgentDefCreated event.Topic = "agent.definition.created"
	TopicAgentDefUpdated event.Topic = "agent.definition.updated"
	TopicAgentDefDeleted event.Topic = "agent.definition.deleted"
)

// AgentService provides CRUD operations on agent definitions with
// automatic synchronization to the in-memory registry and event notification.
type AgentService struct {
	store    AgentStore
	registry *AgentDefinitionRegistry
	bus      *event.Bus
	logger   *zap.Logger
}

// NewAgentService creates a new agent service.
func NewAgentService(store AgentStore, registry *AgentDefinitionRegistry, bus *event.Bus, logger *zap.Logger) *AgentService {
	return &AgentService{
		store:    store,
		registry: registry,
		bus:      bus,
		logger:   logger,
	}
}

// Create creates a new agent definition.
func (s *AgentService) Create(ctx context.Context, def *AgentDefinition) (*AgentDefinition, error) {
	// Check name conflict with registry
	if existing := s.registry.Get(def.Name); existing != nil {
		return nil, fmt.Errorf("agent definition %q already exists", def.Name)
	}

	result, err := s.store.Create(ctx, def)
	if err != nil {
		return nil, fmt.Errorf("create agent definition: %w", err)
	}

	// Sync to in-memory registry
	s.registry.Register(result)
	s.logger.Info("agent definition created", zap.String("name", result.Name))

	// Publish event
	s.bus.PublishAsync(event.Event{
		Topic:   TopicAgentDefCreated,
		Payload: result,
	})

	return result, nil
}

// Get retrieves an agent definition by name.
func (s *AgentService) Get(ctx context.Context, name string) (*AgentDefinition, error) {
	return s.store.Get(ctx, name)
}

// List returns agent definitions matching the filter.
func (s *AgentService) List(ctx context.Context, filter *AgentFilter) ([]*AgentDefinition, error) {
	return s.store.List(ctx, filter)
}

// Update updates an agent definition.
func (s *AgentService) Update(ctx context.Context, name string, updates *AgentUpdate) (*AgentDefinition, error) {
	result, err := s.store.Update(ctx, name, updates)
	if err != nil {
		return nil, fmt.Errorf("update agent definition: %w", err)
	}

	// Sync updated definition to in-memory registry
	s.registry.Register(result)
	s.logger.Info("agent definition updated", zap.String("name", name))

	s.bus.PublishAsync(event.Event{
		Topic:   TopicAgentDefUpdated,
		Payload: result,
	})

	return result, nil
}

// Delete removes an agent definition.
func (s *AgentService) Delete(ctx context.Context, name string) error {
	if err := s.store.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete agent definition: %w", err)
	}

	s.registry.Unregister(name)
	s.logger.Info("agent definition deleted", zap.String("name", name))

	s.bus.PublishAsync(event.Event{
		Topic:   TopicAgentDefDeleted,
		Payload: name,
	})

	return nil
}

// ImportFromYAML loads agent definitions from a YAML directory and persists
// NEW ones to the store. Definitions that already exist in SQLite are skipped
// to avoid overwriting API-made modifications or re-creating deleted agents.
// Returns the number of successfully imported definitions.
func (s *AgentService) ImportFromYAML(ctx context.Context, dir string) (int, []error) {
	tempReg := NewAgentDefinitionRegistry()
	if err := tempReg.LoadFromDirectory(dir); err != nil {
		return 0, []error{fmt.Errorf("load directory %s: %w", dir, err)}
	}

	imported := 0
	var errs []error
	for _, def := range tempReg.All() {
		def.Source = "custom"
		_, err := s.store.Create(ctx, def)
		if err != nil {
			errs = append(errs, fmt.Errorf("import %q: %w", def.Name, err))
			continue
		}
		if err := s.registry.Register(def); err != nil {
			errs = append(errs, fmt.Errorf("register %q: %w", def.Name, err))
			continue
		}
		imported++
	}

	s.logger.Info("YAML import completed",
		zap.String("dir", dir),
		zap.Int("imported", imported),
		zap.Int("errors", len(errs)),
	)
	return imported, errs
}

// SyncBuiltins persists built-in agent definitions to the store.
// Existing built-in entries are updated; new ones are created.
func (s *AgentService) SyncBuiltins(ctx context.Context) error {
	// Ensure builtins are registered in memory first
	s.registry.RegisterBuiltins()

	for _, def := range s.registry.All() {
		// Only sync definitions that look like builtins (no source path)
		if def.Source != "" {
			continue
		}
		def.Source = "builtin"
		def.IsBuiltin = true
		_, err := s.store.Create(ctx, def)
		if err != nil {
			// Already exists — update it
			updates := &AgentUpdate{
				DisplayName:     &def.DisplayName,
				Description:     &def.Description,
				SystemPrompt:    &def.SystemPrompt,
				Model:           &def.Model,
				Profile:         &def.Profile,
				MaxTurns:        &def.MaxTurns,
				Tools:           def.Tools,
				AllowedTools:    def.AllowedTools,
				DisallowedTools: def.DisallowedTools,
				Skills:          def.Skills,
				AutoTeam:        &def.AutoTeam,
				SubAgents:       def.SubAgents,
				Personality:     &def.Personality,
				Triggers:        &def.Triggers,
				IsTeamMember:    &def.IsTeamMember,
			}
			if _, err := s.store.Update(ctx, def.Name, updates); err != nil {
				s.logger.Warn("failed to sync builtin agent definition",
					zap.String("name", def.Name),
					zap.Error(err),
				)
			}
		}
	}
	return nil
}

// LoadAllToRegistry loads all agent definitions from the store into the
// in-memory registry.
//
// Built-in definitions: the SQLite schema doesn't persist every code-side
// field (Tier, OutputSchema, InputSchema, Limitations, ExampleTasks,
// CostTier, Temperature, etc.). Hydrating from SQLite would silently
// strip those, leaving e.g. ListForPlanner with no TierSubAgent matches —
// Plan-mode coordinator would have no skills to dispatch.
//
// Resolution: when a definition is already present in the registry from
// RegisterBuiltins (always called first via SyncBuiltins), we MERGE the
// SQLite-side mutable fields onto it rather than replacing wholesale.
// This keeps DB-driven user edits (DisplayName, Description, etc.)
// effective while preserving the code constants on built-ins.
//
// Non-builtin defs (Source != "builtin", e.g. user-imported YAML) load
// as-is — they have no in-code counterpart to preserve.
func (s *AgentService) LoadAllToRegistry(ctx context.Context) error {
	defs, err := s.store.List(ctx, nil)
	if err != nil {
		return fmt.Errorf("load agent definitions: %w", err)
	}
	loaded := 0
	for _, def := range defs {
		existing := s.registry.Get(def.Name)
		if existing != nil && existing.IsBuiltin {
			// Merge mutable user-editable fields from store onto the
			// in-memory builtin, leaving Tier / OutputSchema / etc.
			// untouched.
			mergeMutableFields(existing, def)
			loaded++
			continue
		}
		if err := s.registry.Register(def); err != nil {
			s.logger.Warn("skipping invalid agent definition",
				zap.String("name", def.Name),
				zap.Error(err),
			)
			continue
		}
		loaded++
	}
	s.logger.Info("loaded agent definitions to registry",
		zap.Int("loaded", loaded),
		zap.Int("total", len(defs)),
	)
	return nil
}

// mergeMutableFields copies SQLite-persisted mutable fields from src to
// dst in place. Used to preserve code-only fields (Tier, OutputSchema,
// etc.) on built-in definitions when re-hydrating from the store.
//
// Field set mirrors what AgentService.Update accepts on the wire — these
// are the "operator can change at runtime" fields. Any field not listed
// here is owned by code (RegisterBuiltins).
func mergeMutableFields(dst, src *AgentDefinition) {
	if src.DisplayName != "" {
		dst.DisplayName = src.DisplayName
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.SystemPrompt != "" {
		dst.SystemPrompt = src.SystemPrompt
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.Profile != "" {
		dst.Profile = src.Profile
	}
	if src.MaxTurns != 0 {
		dst.MaxTurns = src.MaxTurns
	}
	if len(src.Tools) > 0 {
		dst.Tools = src.Tools
	}
	if len(src.AllowedTools) > 0 {
		dst.AllowedTools = src.AllowedTools
	}
	if len(src.DisallowedTools) > 0 {
		dst.DisallowedTools = src.DisallowedTools
	}
	if len(src.Skills) > 0 {
		dst.Skills = src.Skills
	}
	if src.Personality != "" {
		dst.Personality = src.Personality
	}
	if src.Triggers != "" {
		dst.Triggers = src.Triggers
	}
	// IsTeamMember is a bool — always reflect the store value so
	// "remove from team" updates take effect. Tier remains code-owned.
	dst.IsTeamMember = src.IsTeamMember
}
