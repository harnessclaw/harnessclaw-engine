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
		s.registry.Register(def)
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

// LoadAllToRegistry loads all agent definitions from the store into the in-memory registry.
func (s *AgentService) LoadAllToRegistry(ctx context.Context) error {
	defs, err := s.store.List(ctx, nil)
	if err != nil {
		return fmt.Errorf("load agent definitions: %w", err)
	}
	for _, def := range defs {
		s.registry.Register(def)
	}
	s.logger.Info("loaded agent definitions to registry", zap.Int("count", len(defs)))
	return nil
}
