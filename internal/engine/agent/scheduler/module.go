// Package scheduler is the spawn Module for the L2 dispatcher tier.
// emma routes SubagentType=="scheduler" through this module.
//
// Both modes now flow through the v3.1 scheduler kernel
// (enginesched.Coordinator → Scheduler.Submit → dispatch/{react,plan}):
//
//   - react: dispatch/react.Strategy fires one L3 freelancer leaf via
//     SpawnAndWaitOne and returns its meta.json.
//   - plan: dispatch/plan.Strategy runs plan_agent → guard →
//     plan_executor_agent (two leaf phases).
//
// SpawnResult.Output is composed from the meta.json the Coordinator
// wrote (composeOutput) in both modes.
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

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/engine/agent/scheduler/plan"
	"harnessclaw-go/internal/engine/agent/scheduler/react"
	"harnessclaw-go/internal/engine/session"
	schedulertypes "harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/legacy/workspace"
	"harnessclaw-go/pkg/types"
)

// Module is the scheduler tier runtime.
type Module struct {
	deps          Deps
	reactStrategy *react.Strategy
	planStrategy  *plan.Strategy
}

// New constructs a scheduler Module. Runner must be wired with
// WithScheduler(...) — without it both react and plan strategies are
// left nil and Module.Run returns an explicit "requires Deps.Runner"
// error.
func New(deps Deps) *Module {
	m := &Module{deps: deps}
	if deps.Runner != nil {
		m.reactStrategy = react.New(deps.Runner)
		m.planStrategy = plan.New(deps.Runner)
	}
	return m
}

// SubagentType returns "scheduler".
func (m *Module) SubagentType() string { return "scheduler" }

// Run executes the L2 dispatch. Both modes delegate to the v3.1
// scheduler Coordinator; the only difference is Hint.Kind on the
// submitted TaskSpec (react fires one leaf; plan fires plan_agent +
// plan_executor_agent).
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	startTime := time.Now()

	// Bootstrap the on-disk workspace lazily — only when L2 actually
	// runs. plan_update / promote / meta_write all need
	// session/<sid>/plan.json + tasks/ + deliverables/ in place, and
	// L2 is the lowest tier that ever touches them. emma never writes
	// to the workspace, so trivial L1-only queries (e.g. "what's the
	// weather in hefei") leave no on-disk footprint at all.
	rootSID := cfg.RootSessionID
	if rootSID == "" {
		rootSID = cfg.ParentSessionID
	}
	if m.deps.RootDir != "" && rootSID != "" {
		if err := workspace.EnsureSession(m.deps.RootDir, rootSID); err != nil && m.deps.Logger != nil {
			m.deps.Logger.Warn("scheduler module: workspace bootstrap failed",
				zap.String("session_id", rootSID),
				zap.Error(err),
			)
		}
	}

	sess, err := common.BuildSubSession(m.deps.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}
	// mkdir the per-task workspace dir when L2 itself was given a
	// task_id (rare for react, common for plan-mode legacy paths). See
	// common.EnsureTaskDir docstring.
	_ = common.EnsureTaskDir(cfg, m.deps.RootDir)

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

	mode := cfg.CoordinatorMode
	if mode == "" {
		mode = "react"
	}

	if mode == "plan" {
		return m.runPlanLegacy(ctx, cfg, sess, startTime)
	}
	return m.runReactKernel(ctx, cfg, sess, startTime)
}

// runReactKernel routes the dispatch through the v3.1 scheduler kernel
// path (Coordinator → Scheduler.Submit → dispatch/react.Strategy →
// SpawnAndWaitOne → single L3 leaf). The L2 layer no longer drives its
// own LLM loop — the work is done by exactly one freelancer L3
// sub-agent. The parent-visible Output is composed from the meta.json
// the leaf wrote, same shape as runPlanLegacy.
func (m *Module) runReactKernel(ctx context.Context, cfg *agent.SpawnConfig,
	sess *session.Session, startTime time.Time) (*agent.SpawnResult, error) {

	if m.reactStrategy == nil {
		return nil, fmt.Errorf("scheduler module: react mode requires Deps.Runner to be set")
	}

	dispatchSessionID := cfg.ParentSessionID

	metaRef, runErr := m.reactStrategy.Run(ctx, cfg.Prompt, dispatchSessionID, cfg.Model, cfg.ParentOut, sess.ID)

	terminal := types.Terminal{Reason: types.TerminalCompleted}
	if runErr != nil {
		terminal = types.Terminal{
			Reason:  types.TerminalModelError,
			Message: runErr.Error(),
		}
		if m.deps.Logger != nil {
			m.deps.Logger.Debug("scheduler module: react coordinator returned error",
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
	result.CoordinatorMode = "react"
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// runPlanLegacy keeps the legacy plan dispatch path: delegate to
// Coordinator.Run, then read the resulting meta.json off disk to compose
// the parent-visible summary.
func (m *Module) runPlanLegacy(ctx context.Context, cfg *agent.SpawnConfig,
	sess *session.Session, startTime time.Time) (*agent.SpawnResult, error) {

	if m.planStrategy == nil {
		return nil, fmt.Errorf("scheduler module: plan mode requires Deps.Runner to be set")
	}

	dispatchSessionID := cfg.ParentSessionID

	metaRef, runErr := m.planStrategy.Run(ctx, cfg.Prompt, dispatchSessionID, cfg.Model, cfg.ParentOut, sess.ID)

	terminal := types.Terminal{Reason: types.TerminalCompleted}
	if runErr != nil {
		terminal = types.Terminal{
			Reason:  types.TerminalModelError,
			Message: runErr.Error(),
		}
		if m.deps.Logger != nil {
			m.deps.Logger.Debug("scheduler module: plan coordinator returned error",
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
	result.CoordinatorMode = "plan"
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// composeOutput reads the meta.json the Coordinator wrote and composes
// a parent-visible summary string. Used only by the legacy plan path —
// the react LLM-loop path returns LastMessage text directly.
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
// agent_status string used by EmitSubagentEnd.
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
