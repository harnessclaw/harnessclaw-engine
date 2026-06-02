// Package plan_executor_agent runs the plan-mode executor sub-agent. It reads plan.json, dispatches freelancer sub-agents via the freelance tool, and submits the integrated result via submit_task_result.
package plan_executor_agent

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// Deps is the dependency surface plan_executor_agent needs from the host engine.
//
// MaxTokens and ContextWindow live on Deps (not SpawnConfig) because
// they are engine-wide knobs sourced from the parent engine.Config, not
// per-spawn overrides. emma.NewSpawner stamps them once at startup.
type Deps struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger

	// MaxTokens is the per-turn output cap forwarded to the provider.
	// Zero lets the provider default apply.
	MaxTokens int

	// ContextWindow is the model's input window in tokens; the loop's
	// compactor gate uses it.
	ContextWindow int

	// ToolTimeout caps wall-clock for one tool call inside the loop.
	// Zero means "no executor-level cap" — the tool's own internal
	// timeout (e.g. bash's defaultTimeout) still applies.
	ToolTimeout time.Duration

	// LLMAPITimeout caps total wall-clock for one LLM call inside the
	// loop. Zero disables — stuck upstream streams will park the goroutine
	// indefinitely. Tier modules fill this from emma.Config.
	LLMAPITimeout time.Duration

	// LLMFirstByteTimeout cancels when Chat returned but no first chunk
	// arrived within the window. Zero disables. Same provenance.
	LLMFirstByteTimeout time.Duration

	// RootDir is the workspace root (e.g. ~/.harnessclaw/workspace).
	// Combined with cfg.RootSessionID it yields the SessionRoot that
	// meta_write / submit_task_result read from ctx.
	RootDir string
}

// Module is the plan_executor_agent tier runtime.
type Module struct {
	deps Deps
}

// New constructs a plan_executor_agent Module.
func New(deps Deps) *Module {
	return &Module{deps: deps}
}

// SubagentType returns "plan_executor_agent" — the typed key the Spawner
// resolves to route plan-mode spawns at this module.
func (m *Module) SubagentType() string { return "plan_executor_agent" }

// Run executes the plan_executor_agent L3 LLM loop: build sub-session, render
// PlanExecutorAgentProfile system prompt, build tool pool (dispatch tools
// kept because plan_executor_agent dispatches freelancers via the
// freelance tool), emit subagent.start, run loop with StopOnSubmitResult,
// emit subagent.end, return SpawnResult.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	startTime := time.Now()

	sess, err := common.BuildSubSession(m.deps.SessionMgr, cfg.ParentSessionID)
	if err == nil {
		// mkdir the per-task workspace dir so the LLM does not have
		// to shell out a recovery mkdir on its first write — see
		// common.EnsureTaskDir docstring.
		_ = common.EnsureTaskDir(cfg, m.deps.RootDir)
	}
	if err != nil {
		return nil, err
	}

	// plan_executor_agent DOES dispatch (it calls the freelance tool to
	// spawn freelancers), so dispatch tools MUST NOT be stripped.
	// No per-spawn AllowedTools whitelist: SpawnConfig only carries
	// AllowedSkills (skill scoping), not a tool name allowlist.
	// AgentType blacklist is still applied inside BuildToolPool.
	pool := common.BuildToolPool(m.deps.Registry, nil, cfg.AgentType, false)

	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           prompt.PlanExecutorAgentProfile,
		Builder:           m.deps.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      "plan_executor_agent",
		ContextWindow:     m.deps.ContextWindow,
		Registry:          m.deps.Registry,
	})

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    "plan_executor_agent",
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
		ParentStepID:    cfg.ParentStepID,
	})

	ctx = common.WithSubAgentStats(ctx, sess.ID, sess.ID,
		cfg.ParentSessionID, cfg.RootSessionID)

	// Seed session with the prompt as the first user message.
	sess.AddMessage(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText, Text: common.SeedPrompt(cfg, m.deps.RootDir),
		}},
	})

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		// plan_executor_agent typically needs more turns than a generic worker
		// to read context, analyze, and write plan.json.
		maxTurns = 20
	}

	// Sub-agents inherit the parent session's approved tool whitelist.
	// common.BuildInheritedChecker returns BypassChecker when no
	// approved tools (sub-agents have no UI to ask).
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
		AgentScope:     common.BuildAgentScope(cfg, m.deps.RootDir, "plan_executor_agent"),
		OnTurnComplete: common.StopOnSubmitResult(),
	})

	if err != nil {
		return nil, err
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
		SubagentType:    "plan_executor_agent",
		DurationMs:      time.Since(startTime).Milliseconds(),
		Usage:           &usage,
		Terminal:        &terminal,
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
	})

	return common.BuildSpawnResult(sess.ID, sess.ID, output, terminal, usage, loopRes.NumTurns), nil
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
