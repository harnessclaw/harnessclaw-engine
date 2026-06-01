// Package plan_design runs the design / methodology planning sub-agent (the "Plan" agent in user-facing roster). NOT the plan-mode plan_agent — this one designs approaches without writing plan.json. Terminates on natural end_turn.
package plan_design

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

// Deps is the dependency surface plan_design needs from the host engine.
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
}

// Module is the plan_design tier runtime.
type Module struct {
	deps Deps
}

// New constructs a plan_design Module.
func New(deps Deps) *Module {
	return &Module{deps: deps}
}

// SubagentType returns "plan" — the registry key the Spawner resolves
// to route design / methodology planning spawns at this module. Note
// the Go package is plan_design (since "plan" is reserved for the
// plan-mode plan_agent), but the user-facing roster key is "plan".
func (m *Module) SubagentType() string { return "plan" }

// Run executes the plan_design L3 LLM loop: build sub-session, render
// PlanProfile system prompt, build tool pool (dispatch tools stripped
// — plan_design is a strict leaf), emit subagent.start, run loop with
// StopOnEndTurn (no submit_task_result contract — design agents
// terminate on natural end_turn), emit subagent.end, return
// SpawnResult.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	startTime := time.Now()

	sess, err := common.BuildSubSession(m.deps.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}

	// plan_design is L3 leaf: strip dispatch tools so it cannot
	// recursively spawn. No per-spawn AllowedTools whitelist:
	// SpawnConfig only carries AllowedSkills (skill scoping), not a
	// tool name allowlist. AgentType blacklist is still applied inside
	// BuildToolPool.
	pool := common.BuildToolPool(m.deps.Registry, nil, cfg.AgentType, true)

	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           prompt.PlanProfile,
		Builder:           m.deps.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      "plan",
		ContextWindow:     m.deps.ContextWindow,
		Registry:          m.deps.Registry,
	})

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    "plan",
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
			Type: types.ContentTypeText, Text: cfg.Prompt,
		}},
	})

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		// plan_design designs approaches without implementation; 15
		// turns is the middle ground between explore (10) and
		// plan_agent (20).
		maxTurns = 15
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
		Out:            cfg.ParentOut,
		AgentID:        sess.ID,
		PermChecker:    permChecker,
		ApprovalFn:     nil, // sub-agents have no approval UI
		OnTurnComplete: common.StopOnEndTurn(),
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
		SubagentType:    "plan",
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
