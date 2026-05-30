package scheduler

import (
	"go.uber.org/zap"

	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn"
)

// Deps is the dependency surface the scheduler (L2 dispatcher) module
// needs from the host engine.
//
// scheduler is unique among tier modules: it does NOT drive its own LLM
// loop. Instead it dispatches to the legacy enginesched.Coordinator,
// which owns its own msgbus, kernel, tstate stores, ConsumerPool and a
// LLMKindSelector. The Coordinator already supports both react and plan
// kinds via that internal pipeline, and rebuilding that machinery inside
// agent/scheduler would duplicate a substantial subsystem with no
// migration benefit during Stage 7.
//
// This module therefore acts as a thin spawn.Module adapter:
//
//  1. emit subagent.start through common.EmitSubagentStart
//  2. translate cfg.CoordinatorMode → spec.TaskSpec.Hint.Kind
//  3. delegate to Coord.Run, forwarding events through ParentOut
//  4. assemble the SpawnResult from the resulting MetaRef (mirrors the
//     legacy spawn package's metaRefToLoopResult helper)
//  5. emit subagent.end
//
// The Spawner field is reserved for the future port of the in-module
// plan strategy (DONE_WITH_CONCERNS #1 below): once we drop the legacy
// msgbus/kernel pipeline, the plan strategy will dispatch plan_agent and
// plan_executor_agent through this *spawn.Spawner directly instead of
// round-tripping through msgbus → subagent.QueryEngineFactory →
// emma.SpawnSync. Until then it sits unused; we still take it on Deps so
// emma can wire it once and we never have to touch core.go again when
// Stage 8 cleanup flips the implementation.
type Deps struct {
	// Coord is the legacy L2 scheduler Coordinator that owns the
	// msgbus / tstate / kernel pipeline implementing react + plan.
	// scheduler.Module forwards every Run call to Coord.Run.
	Coord *enginesched.Coordinator

	// SessionMgr is used to derive a sub-session for the dispatch.
	// Mirrors what other tier modules (plan_agent, freelancer, ...) do.
	SessionMgr *session.Manager

	// WorkspaceRoot is the absolute workspace root directory. Used to
	// read the meta.json the Coordinator writes once a task succeeds, so
	// we can hand the summary + output paths back to the parent via
	// SpawnResult.Output. Empty disables meta loading and the parent
	// sees a generic completion message.
	WorkspaceRoot string

	// Logger is used for diagnostic logging inside Run.
	Logger *zap.Logger

	// Spawner is reserved for the in-module plan strategy port. See the
	// package comment in deps.go. nil is acceptable today.
	Spawner *spawn.Spawner
}
