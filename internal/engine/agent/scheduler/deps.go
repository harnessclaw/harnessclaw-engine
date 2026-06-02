package scheduler

import (
	"go.uber.org/zap"

	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
)

// Deps is the dependency surface the scheduler (L2 dispatcher) module
// needs from the host engine.
//
// Both react and plan modes delegate to the v3.1 scheduler kernel
// (enginesched.Coordinator), so the module itself no longer needs the
// LLM stack (Provider / Registry / Compactor / Retryer / PromptBuilder
// etc.) — those live inside Coord's QueryEngineFactory.
type Deps struct {
	Logger *zap.Logger

	// SessionMgr is used to build the per-spawn sub-session.
	SessionMgr *session.Manager

	// RootDir is the workspace root (e.g. ~/.harnessclaw/workspace).
	// Required for workspace.EnsureSession + per-task dir bootstrap.
	RootDir string

	// WorkspaceRoot mirrors RootDir for the composeOutput helper that
	// reads the Coordinator-written meta.json off disk.
	WorkspaceRoot string

	// Coord is the v3.1 L2 scheduler Coordinator. Required for both
	// react and plan modes — without it Module.Run returns an explicit
	// "requires Deps.Coord" error.
	Coord *enginesched.Coordinator
}
