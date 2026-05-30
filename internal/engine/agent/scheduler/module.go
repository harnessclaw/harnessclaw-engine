// Package scheduler is the spawn2 Module for the L2 dispatcher tier.
// emma routes SubagentType=="scheduler" through this module; the module
// delegates to one of two strategies (react, plan) chosen by
// cfg.CoordinatorMode.
//
// Stage 7 wraps the legacy enginesched.Coordinator instead of porting
// the full L2 pipeline. See package deps.go and the strategy packages
// for rationale and the planned Stage-8 follow-up.
//
// Event flow stays parity-compatible with the legacy spawn path:
//
//   - subagent.start emitted before the dispatch starts
//   - Coordinator forwards L3 lifecycle events (start/end/tool calls,
//     intents) through cfg.ParentOut while the dispatch runs
//   - subagent.end emitted after the dispatch returns with the final
//     terminal + usage envelope
//
// SpawnResult.Output is composed from the meta.json the Coordinator
// wrote to the workspace (mirrors the legacy metaRefToLoopResult helper
// in spawn.go) so the parent (emma) sees the same summary +
// deliverables block whether the dispatch ran through legacy spawn or
// the new module.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/agent/scheduler/plan"
	"harnessclaw-go/internal/engine/agent/scheduler/react"
	schedulertypes "harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// Module is the scheduler tier runtime.
type Module struct {
	deps           Deps
	reactStrategy  *react.Strategy
	planStrategy   *plan.Strategy
}

// New constructs a scheduler Module. Coord may be nil for tests that
// only exercise SubagentType(); Run requires Coord to be non-nil.
func New(deps Deps) *Module {
	m := &Module{deps: deps}
	if deps.Coord != nil {
		m.reactStrategy = react.New(deps.Coord)
		m.planStrategy = plan.New(deps.Coord)
	}
	return m
}

// SubagentType returns "scheduler" — the typed key the Spawner resolves
// to route scheduler spawns at this module.
func (m *Module) SubagentType() string { return "scheduler" }

// Run dispatches the cfg to the underlying Coordinator according to
// cfg.CoordinatorMode (react default, plan when explicitly pinned).
// Emits subagent.start/end on cfg.ParentOut around the dispatch and
// composes a SpawnResult from the resulting MetaRef.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	if m.deps.Coord == nil {
		return nil, fmt.Errorf("scheduler module: Coord dependency is nil")
	}

	startTime := time.Now()

	sess, err := common.BuildSubSession(m.deps.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    "scheduler",
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
		ParentStepID:    cfg.ParentStepID,
	})

	ctx = common.WithSubAgentStats(ctx, sess.ID, sess.ID,
		cfg.ParentSessionID, cfg.RootSessionID)

	// Reset session id on the spec to the PARENT session id rather than
	// the sub-session we just built — the legacy Coordinator anchors all
	// workspace writes (plan.json, meta.json, deliverables/) under the
	// parent session so emma's per-session bootstrap (EnsureSession in
	// ProcessMessage) lines up with where the dispatch will write.
	dispatchSessionID := cfg.ParentSessionID

	mode := cfg.CoordinatorMode
	if mode == "" {
		mode = "react"
	}

	var (
		metaRef schedulertypes.MetaRef
		runErr  error
	)
	switch mode {
	case "plan":
		metaRef, runErr = m.planStrategy.Run(ctx, cfg.Prompt, dispatchSessionID, cfg.Model, cfg.ParentOut)
	default:
		metaRef, runErr = m.reactStrategy.Run(ctx, cfg.Prompt, dispatchSessionID, cfg.Model, cfg.ParentOut)
		mode = "react"
	}

	terminal := types.Terminal{Reason: types.TerminalCompleted}
	if runErr != nil {
		terminal = types.Terminal{
			Reason:  types.TerminalModelError,
			Message: runErr.Error(),
		}
		if m.deps.Logger != nil {
			m.deps.Logger.Debug("scheduler module: coordinator returned error",
				zap.String("mode", mode),
				zap.Error(runErr),
			)
		}
	}

	output := composeOutput(m.deps.WorkspaceRoot, dispatchSessionID, metaRef)

	usage := types.Usage{}

	common.EmitSubagentEnd(cfg.ParentOut, common.EndEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentStatus:     statusFromTerminal(terminal),
		SubagentType:    "scheduler",
		DurationMs:      time.Since(startTime).Milliseconds(),
		Usage:           &usage,
		Terminal:        &terminal,
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
	})

	result := common.BuildSpawnResult(sess.ID, sess.ID, output, terminal, usage, 0)
	// Stamp the L2 coordinator surface so the parent sees which mode ran
	// — parity with the legacy spawn path (spawn.go line 1021).
	result.CoordinatorMode = mode
	if runErr != nil {
		// Surface the error as-is so the spawn2 façade can route it
		// through the standard "module returned error" path. The
		// subagent.end event has already been emitted above, so the
		// parent has the terminal envelope before the error pops.
		return result, runErr
	}
	return result, nil
}

// composeOutput reads the meta.json the Coordinator wrote and composes
// a parent-visible summary string. Mirrors the legacy
// metaRefToLoopResult helper from spawn.go so the parent sees the same
// summary + deliverables block.
//
// Returns "" when any required input is missing or the meta is
// unreadable — the parent then sees a generic completion message rather
// than a synthetic error.
func composeOutput(rootDir, sessionID string, ref schedulertypes.MetaRef) string {
	if rootDir == "" || sessionID == "" || string(ref) == "" {
		return ""
	}
	absPath := filepath.Join(workspace.SessionRoot(rootDir, sessionID), string(ref))
	b, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	var m workspace.Meta
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.Summary)
	if len(m.Outputs) > 0 {
		sb.WriteString("\n产出文件：\n")
		for _, o := range m.Outputs {
			if o.Path != "" {
				sb.WriteString("- ")
				sb.WriteString(o.Path)
				sb.WriteString("\n")
			}
		}
	}
	delivDir := workspace.DeliverablesDir(rootDir, sessionID)
	if entries, err := os.ReadDir(delivDir); err == nil && len(entries) > 0 {
		sb.WriteString("\n交付目录（已整理至此）：")
		sb.WriteString(delivDir)
		sb.WriteString("\n")
	}
	return sb.String()
}

// statusFromTerminal maps Terminal.Reason to the wire envelope's
// agent_status string used by EmitSubagentEnd. Mirrors the helper used
// by every other tier module.
func statusFromTerminal(t types.Terminal) string {
	switch t.Reason {
	case types.TerminalCompleted:
		return "completed"
	case types.TerminalMaxTurns:
		return "max_turns"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		return "aborted"
	default:
		return "failed"
	}
}
