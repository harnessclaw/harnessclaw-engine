package queryloop

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/event"
)

// Deps is the dependency surface QueryEngine implements so queryloop.Runner
// can do its work.
//
// First-draft scope: methods listed below cover the bare minimum identified
// during scaffolding. Task 5.4 widens this as code migrates in.
type Deps interface {
	Logger() *zap.Logger
	EventBus() *event.Bus

	SessionMgr() *session.Manager

	// Sub-service handles
	Spawner() *spawn.Spawner

	// Cancellation registry — Runner manages per-session cancel context.
	RegisterCancel(sid string, cancel context.CancelFunc)
	DeregisterCancel(sid string)
}

// Runner drives one user turn. Constructed once per engine, reused across
// ProcessMessage calls. Internal state is per-session and lives in the
// session, not on Runner.
type Runner struct {
	deps Deps
}

// NewRunner constructs a Runner backed by the given Deps. Deps must remain
// valid for the Runner's lifetime — typically Deps is the parent QueryEngine.
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}

// Compile-time guard: prevents unused-import errors when the file is new
// and Runner has no methods yet. Removed in Task 5.4 when real methods land.
var _ context.Context = context.Background()
