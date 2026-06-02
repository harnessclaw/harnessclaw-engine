// Package scheduler is the spawn Module for the L2 dispatcher tier.
// emma routes SubagentType=="scheduler" through this module.
//
// Two modes:
//
//   - react (default): the module drives its own LLM loop with a palette
//     of [freelance, plan_*, meta_*, submit_task_result, read/glob/grep,
//     web_search/tavily_search]. The L2 LLM plans/dispatches/integrates/
//     checks per principles/scheduler.go. Terminates on submit_task_result.
//   - plan: still wraps the legacy enginesched.Coordinator's plan path
//     (plan_agent → guard → plan_executor_agent). Stage 8 will port this
//     in-module too.
//
// SpawnResult.Output:
//
//   - react: composed from the loop's terminal LastMessage text, mirroring
//     plan_executor_agent. submit_task_result is the canonical terminal,
//     so meta.json was written by L2 itself via meta_write.
//   - plan: read from meta.json the Coordinator wrote (composeOutput).
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
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	schedulertypes "harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// Module is the scheduler tier runtime.
type Module struct {
	deps          Deps
	reactStrategy *react.Strategy
	planStrategy  *plan.Strategy
}

// New constructs a scheduler Module. Coord may be nil if only react mode
// is exercised; plan mode requires Coord to be non-nil.
func New(deps Deps) *Module {
	m := &Module{deps: deps}
	if deps.Coord != nil {
		m.reactStrategy = react.New(deps.Coord)
		m.planStrategy = plan.New(deps.Coord)
	}
	return m
}

// SubagentType returns "scheduler".
func (m *Module) SubagentType() string { return "scheduler" }

// Run executes the L2 dispatch. react mode drives an LLM loop in-module;
// plan mode delegates to the legacy Coordinator.
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
	return m.runReactLLM(ctx, cfg, sess, startTime)
}

// runReactLLM drives the L2 LLM loop. Tool palette intentionally
// includes file inspection (read/glob/grep) and web search so the
// scheduler can do its own exploration before/while dispatching L3 —
// emma forbids exploratory dispatches, so this layer absorbs that
// responsibility.
func (m *Module) runReactLLM(ctx context.Context, cfg *agent.SpawnConfig,
	sess *session.Session, startTime time.Time) (*agent.SpawnResult, error) {

	// Pool must be built from the scheduler AgentDefinition.AllowedTools
	// whitelist, NOT from the AgentType blacklist. Reason: `freelance`
	// and `scheduler` are in AllAgentDisallowed (see
	// tool/restrictions.go) and would be stripped by the blacklist —
	// then the LLM, prompted by scheduler principles to call freelance,
	// hallucinates the call and toolexec returns "unknown tool". The
	// whitelist in AgentDefinition explicitly allows freelance for L2.
	var allowed []string
	if m.deps.DefRegistry != nil {
		if def := m.deps.DefRegistry.Get("scheduler"); def != nil {
			allowed = def.AllowedTools
		}
	}
	pool := common.BuildToolPool(m.deps.Registry, allowed, cfg.AgentType, false)

	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           prompt.SchedulerProfile,
		Builder:           m.deps.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      "scheduler",
		ContextWindow:     m.deps.ContextWindow,
		Registry:          m.deps.Registry,
	})

	// Seed session with the prompt as the first user message.
	sess.AddMessage(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText, Text: common.SeedPrompt(cfg, m.deps.RootDir),
		}},
	})

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		// scheduler runs plan/dispatch/integrate/check cycles; needs
		// more turns than a leaf worker.
		maxTurns = 30
	}

	permChecker := common.BuildInheritedChecker(
		common.SessionApprovedTools(m.deps.SessionMgr, cfg.ParentSessionID),
	)

	loopRes, err := loop.Run(ctx, &loop.Config{
		Session:        sess,
		SystemPrompt:   sysPrompt,
		Tools:          pool,
		Provider:       m.deps.Provider,
		Compactor:      m.deps.Compactor,
		Retryer:        m.deps.Retryer,
		Logger:         m.deps.Logger,
		MaxTurns:       maxTurns,
		MaxTokens:      m.deps.MaxTokens,
		ContextWindow:  m.deps.ContextWindow,
		ToolTimeout:    m.deps.ToolTimeout,
		LLMAPITimeout:       m.deps.LLMAPITimeout,
		LLMFirstByteTimeout: m.deps.LLMFirstByteTimeout,
		Out:            cfg.ParentOut,
		AgentID:        sess.ID,
		PermChecker:    permChecker,
		ApprovalFn:     nil, // sub-agents have no approval UI
		AgentScope: common.BuildAgentScope(cfg, m.deps.RootDir, "scheduler"),
		// react mode terminates on natural end_turn (no tool calls).
		// Unlike L3 workers, L2 has no per-spawn task_id assigned by emma
		// (the scheduler tool doesn't allocate one), so submit_task_result
		// would fail; StopOnSubmitResult would loop until MaxTurns. The
		// last assistant text becomes the parent-visible summary —
		// scheduler principles guide the LLM to format that message as
		// the final report to emma.
		OnTurnComplete: common.StopOnEndTurn(),
	})

	if err != nil {
		// Module-level error (not LLM error): emit end + return.
		terminal := types.Terminal{Reason: types.TerminalModelError, Message: err.Error()}
		common.EmitSubagentEnd(cfg.ParentOut, common.EndEvent{
			AgentID:         sess.ID,
			AgentName:       cfg.Name,
			AgentStatus:     statusFromTerminal(terminal),
			SubagentType:    "scheduler",
			DurationMs:      time.Since(startTime).Milliseconds(),
			Terminal:        &terminal,
			ParentAgentID:   cfg.ParentAgentID,
			ParentSessionID: cfg.ParentSessionID,
		})
		result := common.BuildSpawnResult(sess.ID, sess.ID, "", terminal, types.Usage{}, 0)
		result.CoordinatorMode = "react"
		return result, err
	}

	output := ""
	if loopRes.LastMessage != nil {
		for _, b := range loopRes.LastMessage.Content {
			if b.Type == types.ContentTypeText {
				output += b.Text
			}
		}
	}

	terminal := loopRes.Terminal
	usage := loopRes.Usage

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

	result := common.BuildSpawnResult(sess.ID, sess.ID, output, terminal, usage, loopRes.NumTurns)
	result.CoordinatorMode = "react"
	return result, nil
}

// runPlanLegacy keeps the legacy plan dispatch path: delegate to
// Coordinator.Run, then read the resulting meta.json off disk to compose
// the parent-visible summary.
func (m *Module) runPlanLegacy(ctx context.Context, cfg *agent.SpawnConfig,
	sess *session.Session, startTime time.Time) (*agent.SpawnResult, error) {

	if m.planStrategy == nil {
		return nil, fmt.Errorf("scheduler module: plan mode requires Deps.Coord to be set")
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
