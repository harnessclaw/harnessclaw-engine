package spawn

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
)

// Deps is the dependency surface that the parent engine must implement
// so spawn.Spawner can do its work. Kept intentionally narrow — adding
// a new field here means spawner needs access to new engine state.
//
// First-draft scope: the methods listed below cover what subagent.go
// currently reads from *QueryEngine. Task 2.2 will refine after the
// actual code migration reveals the precise surface.
type Deps interface {
	Logger() *zap.Logger
	Config() SpawnConfig

	Provider() provider.Provider
	Registry() *tool.Registry
	CmdRegistry() *command.Registry
	Compactor() compact.Compactor
	PermChecker() permission.Checker
	EventBus() *event.Bus

	SessionMgr() *session.Manager
	StatsRegistry() *sessionstats.Registry
	DefRegistry() *agent.AgentDefinitionRegistry
	SkillReader() *skill.Reader

	PromptBuilder() *prompt.Builder
	SchedulerCoord() *enginesched.Coordinator

	// SelfSpawner returns the parent AgentSpawner so sub-agents can
	// recursively spawn (e.g., L2 → L3). The parent supplies itself —
	// QueryEngine implements agent.AgentSpawner via SpawnSync.
	SelfSpawner() agent.AgentSpawner
}

// SpawnConfig is the spawn-relevant subset of engine.QueryEngineConfig.
// Returned as a value snapshot from Deps.Config() so spawn code never
// holds a pointer into the engine's mutable state.
//
// First-draft field set: refined in Task 2.2 once code migration
// reveals which engine.QueryEngineConfig fields spawn actually reads.
type SpawnConfig struct {
	MaxTurns      int
	ToolTimeout   time.Duration
	MaxTokens     int
	ContextWindow int
	ClientTools   bool
}

// Spawner runs sub-agents on behalf of the parent engine. Owns the
// 14-step SpawnSync pipeline and the per-agent taskRegistry cache.
//
// First-draft scaffold: SpawnSync method body is added in Task 2.2.
type Spawner struct {
	deps Deps

	// taskRegistry stores completed sub-agent results by agentID.
	// Used for context passing (depends_on) and debugging.
	// TODO: add TTL or LRU eviction to prevent unbounded growth.
	taskRegistryMu sync.RWMutex
	taskRegistry   map[string]*agent.SpawnResult
}

// NewSpawner constructs a Spawner backed by the given Deps. Deps must
// remain valid for the Spawner's lifetime — typically Deps is the
// parent QueryEngine, which lives for the process lifetime.
func NewSpawner(deps Deps) *Spawner {
	return &Spawner{
		deps:         deps,
		taskRegistry: make(map[string]*agent.SpawnResult),
	}
}

// Compile-time guard: prevents unused-import errors when the file is
// new and Spawner has no methods yet. Removed in Task 2.2 when real
// methods land.
var _ context.Context = context.Background()
