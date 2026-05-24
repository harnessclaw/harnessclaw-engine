package subagent

import (
	"context"
	"os"
	"path/filepath"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/workspace"
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

// Build implements ContextFactory.
func (f *QueryEngineFactory) Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) LeafContext {
	taskDir := workspace.TaskDir(f.rootDir, sessionID, string(taskID))
	_ = os.MkdirAll(taskDir, 0o755)

	ws := &flatWorkspace{
		taskDir:     taskDir,
		sessionRoot: workspace.SessionRoot(f.rootDir, sessionID),
	}

	cfg := specToSpawnConfig(sp, f.rootSessionID)
	cfg.TaskID = string(taskID)

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
		ParentSessionID: sp.SessionID,
		RootSessionID:   rootSessionID,
	}
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
