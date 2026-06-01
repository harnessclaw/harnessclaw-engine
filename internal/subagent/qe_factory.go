package subagent

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/scheduler/router"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/workspace"
	pkgtypes "harnessclaw-go/pkg/types"
)

// QueryEngineFactory implements ContextFactory by wiring a real AgentSpawner
// into the LeafContext's SpawnFn. Use NewQueryEngineFactory for production;
// the staging and bus fields are optional and can be added via WithStagingAndBus.
type QueryEngineFactory struct {
	spawner       agent.AgentSpawner
	rootDir       string
	rootSessionID string
	staging       tstate.StagingWriter
	bus           msgbus.Bus
	agentResolver router.AgentResolver // optional

	outMu sync.RWMutex
	outCh chan<- pkgtypes.EngineEvent // current Run's event sink; nil when idle
}

// SetOutCh registers the event channel for an active Run call so that L3
// sub-agent events (tool calls, start/end) are forwarded to the client.
// Call before sched.Submit; defer SetOutCh(nil) to clear after Run returns.
func (f *QueryEngineFactory) SetOutCh(ch chan<- pkgtypes.EngineEvent) {
	f.outMu.Lock()
	defer f.outMu.Unlock()
	f.outCh = ch
}

func (f *QueryEngineFactory) currentOutCh() chan<- pkgtypes.EngineEvent {
	f.outMu.RLock()
	defer f.outMu.RUnlock()
	return f.outCh
}

// NewQueryEngineFactory creates a QueryEngineFactory.
// rootDir is the workspace root (e.g. workspace.DefaultRootDir()).
// rootSessionID is the top-level session id (used for RootSessionID in SpawnConfig).
func NewQueryEngineFactory(spawner agent.AgentSpawner, rootDir, rootSessionID string) *QueryEngineFactory {
	return &QueryEngineFactory{
		spawner:       spawner,
		rootDir:       rootDir,
		rootSessionID: rootSessionID,
	}
}

// WithStagingAndBus attaches optional staging and bus references.
// Returns the same factory for fluent chaining.
func (f *QueryEngineFactory) WithStagingAndBus(staging tstate.StagingWriter, bus msgbus.Bus) *QueryEngineFactory {
	f.staging = staging
	f.bus = bus
	return f
}

// WithAgentResolver attaches an optional AgentResolver that selects which
// named subagent profile to use when the spec does not specify one.
// Returns the same factory for fluent chaining.
func (f *QueryEngineFactory) WithAgentResolver(r router.AgentResolver) *QueryEngineFactory {
	f.agentResolver = r
	return f
}

// Build implements ContextFactory.
func (f *QueryEngineFactory) Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) LeafContext {
	taskDir := workspace.TaskDir(f.rootDir, sessionID, string(taskID))
	_ = os.MkdirAll(taskDir, 0o755)

	ws := &flatWorkspace{
		taskDir:     taskDir,
		sessionRoot: workspace.SessionRoot(f.rootDir, sessionID),
	}

	// rootSessionID priority: factory-bound (set by Coordinator if
	// known at startup) → TaskSpec.SessionID (set by strategies that
	// know the user-facing session). The Coordinator currently
	// constructs the factory with "" (one Coordinator serves all
	// sessions), so sp.SessionID is the real source. Without this
	// fallback every L3 SpawnConfig.RootSessionID is empty,
	// AgentScope.SessionRoot is empty, and meta_write /
	// submit_task_result / plan_read fail with "SessionRoot missing".
	rootSID := f.rootSessionID
	if rootSID == "" {
		rootSID = sp.SessionID
	}
	cfg := specToSpawnConfig(sp, rootSID)
	cfg.TaskID = string(taskID)
	// ParentAgentID comes from the L2 caller via sp.ParentAgentID. The
	// translator uses this to parent the L3 card under the L2 card;
	// without it L3 falls back to the grandparent's tool card and the
	// UI shows L2/L3 as siblings.
	cfg.ParentAgentID = sp.ParentAgentID
	// Use agent resolver to fill SubagentType if not already set.
	if cfg.SubagentType == "" && f.agentResolver != nil {
		cfg.SubagentType = f.agentResolver.Resolve(sp.Goal, knownAgents())
	}
	if cfg.SubagentType == "" {
		cfg.SubagentType = "freelancer"
	}
	cfg.Name = cfg.SubagentType
	cfg.ParentOut = f.currentOutCh()

	spawner := f.spawner
	spawnFn := SpawnFn(func(ctx context.Context) (*agent.SpawnResult, error) {
		return spawner.SpawnSync(ctx, cfg)
	})

	return LeafContext{
		TaskID:    taskID,
		SessionID: sessionID,
		SpecRef:   sp,
		Model:     sp.Model,
		Workspace: ws,
		Staging:   f.staging,
		Bus:       f.bus,
		SpawnFn:   spawnFn,
	}
}

// specToSpawnConfig maps a spec.TaskSpec to an *agent.SpawnConfig.
func specToSpawnConfig(sp spec.TaskSpec, rootSessionID string) *agent.SpawnConfig {
	return &agent.SpawnConfig{
		Prompt:          sp.Goal,
		Model:           sp.Model,
		SubagentType:    sp.SubagentType,
		ParentSessionID: sp.SessionID,
		RootSessionID:   rootSessionID,
		InputPaths:      sp.InputPaths,
	}
}

// knownAgents returns the registered agent types eligible for Phase 3 L3 dispatch.
// IMPORTANT: only include names that are registered in AgentDefinitionRegistry.RegisterBuiltins().
// Unregistered names resolve to agentDef=nil → isSubAgent=false → coordinator branch.
func knownAgents() []string {
	return []string{"freelancer", "plan_agent", "plan_executor_agent"}
}

// flatWorkspace implements WorkspaceHandle for a flat (per-session) layout.
type flatWorkspace struct {
	taskDir     string
	sessionRoot string
}

func (w *flatWorkspace) TaskDir() string    { return w.taskDir }
func (w *flatWorkspace) MetaPath() string   { return filepath.Join(w.taskDir, "meta.json") }
func (w *flatWorkspace) MetaRelPath() string {
	rel, err := filepath.Rel(w.sessionRoot, w.MetaPath())
	if err != nil {
		return "meta.json"
	}
	return rel
}
func (w *flatWorkspace) ReadScope() []string  { return []string{w.taskDir} }
func (w *flatWorkspace) WriteScope() []string { return []string{w.taskDir} }
func (w *flatWorkspace) InputPaths() []string { return nil }

func (w *flatWorkspace) WriteFile(_ context.Context, relPath string, data []byte) error {
	abs := filepath.Join(w.taskDir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func (w *flatWorkspace) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.taskDir, relPath))
}

func (w *flatWorkspace) WriteMeta(ctx context.Context, m workspace.Meta) (string, error) {
	return WriteMeta(ctx, w.taskDir, w.sessionRoot, m)
}
