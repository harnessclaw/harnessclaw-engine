package scheduler

import (
	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/engine/session"
)

// Deps is the dependency surface the scheduler (L2 dispatcher) module
// needs from the host engine.
//
// Both react and plan modes delegate to agentrun.ModeScheduled (which
// wraps the v3.1 scheduler kernel under the hood), so the module itself
// no longer needs the LLM stack (Provider / Registry / Compactor /
// Retryer / PromptBuilder etc.) — those live inside the agentrun
// Runner's scheduler backend (worker.Factory).
type Deps struct {
	Logger *zap.Logger

	// SessionMgr is used to build the per-spawn sub-session.
	SessionMgr *session.Manager

	// RootDir is the workspace root (e.g. ~/.harnessclaw/workspace).
	// Required for workspace.EnsureSession + per-task dir bootstrap.
	RootDir string

	// WorkspaceRoot mirrors RootDir for the composeOutput helper that
	// reads the scheduler-written meta.json off disk.
	WorkspaceRoot string

	// Runner is the agentrun dispatcher (with WithScheduler(...) wired
	// to a Coordinator). Required for both react and plan modes —
	// without it Module.Run returns an explicit "requires Deps.Runner"
	// error.
	Runner *agentrun.Runner
}
