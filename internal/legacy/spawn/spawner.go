package spawn

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
)

// Spawner is the routing primitive for sub-agent spawning.
//
// Modules register themselves under their SubagentType. Sync routes by
// cfg.SubagentType; Async wraps Sync in a goroutine and returns a
// Handle. spawn.Spawner satisfies agent.AgentSpawner.
type Spawner struct {
	mu       sync.RWMutex
	modules  map[string]Module
	fallback Module
	logger   *zap.Logger
}

// NewSpawner returns an empty Spawner. Caller must Register modules
// before calling Sync.
func NewSpawner(logger *zap.Logger) *Spawner {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Spawner{
		modules: make(map[string]Module),
		logger:  logger,
	}
}

// Register adds a module under its declared SubagentType. Panics on
// duplicate registration so the error surfaces at startup.
func (s *Spawner) Register(m Module) {
	key := m.SubagentType()
	if key == "" {
		panic("spawn: Module.SubagentType() returned empty string")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.modules[key]; exists {
		panic(fmt.Sprintf("%v: %q", ErrDuplicateRegistration, key))
	}
	s.modules[key] = m
}

// SetFallback installs a module used when SubagentType has no specific
// registration. Default behavior (no fallback) returns
// ErrUnknownSubagentType. Call this only in environments where silent
// fallback to a generic runner is acceptable (testing / dev).
func (s *Spawner) SetFallback(m Module) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = m
}

// Sync routes the spawn to the module registered for cfg.SubagentType.
// On unknown type with no fallback, returns ErrUnknownSubagentType.
func (s *Spawner) Sync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	s.mu.RLock()
	m, ok := s.modules[cfg.SubagentType]
	if !ok {
		m = s.fallback
	}
	s.mu.RUnlock()

	if m == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSubagentType, cfg.SubagentType)
	}
	return m.Run(ctx, cfg)
}

// SpawnSync implements agent.AgentSpawner so tools can use Spawner via
// the contract interface.
func (s *Spawner) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return s.Sync(ctx, cfg)
}

// Compile-time interface satisfaction check.
var _ agent.AgentSpawner = (*Spawner)(nil)
