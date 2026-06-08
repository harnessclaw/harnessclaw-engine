// Package plan_executor_agent runs the plan-mode executor sub-agent:
// reads plan.json, dispatches freelancers via the freelance tool for
// each step, and integrates results.
//
// As of the data-driven runner migration this file is a thin shim that
// forwards to engine/runner.Runner.RunLeaf with builtin.PlanExecutor.
// Behaviour is unchanged: PlanExecutorAgentProfile, dispatch tools
// retained (the executor MUST be able to call freelance), default
// 20 turns, StopOnSubmitResult hook, scope label
// "plan_executor_agent", SubagentType pinned to "plan_executor_agent".
package plan_executor_agent

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/engine/agent/runAgent/runner"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
)

// Deps mirrors the original Deps struct so emma.NewSpawner wiring
// compiles untouched.
type Deps struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger

	MaxTokens           int
	ContextWindow       int
	ToolTimeout         time.Duration
	LLMAPITimeout       time.Duration
	LLMFirstByteTimeout time.Duration
	RootDir             string
}

// Module is the plan_executor_agent tier runtime — now a thin runner
// wrapper.
type Module struct {
	rt *runner.Runner
}

// New constructs a plan_executor_agent Module backed by a runner.Runner.
func New(deps Deps) *Module {
	return &Module{
		rt: runner.New(runner.Deps{
			Provider:            deps.Provider,
			Registry:            deps.Registry,
			SessionMgr:          deps.SessionMgr,
			Compactor:           deps.Compactor,
			Retryer:             deps.Retryer,
			PromptBuilder:       deps.PromptBuilder,
			Logger:              deps.Logger,
			MaxTokens:           deps.MaxTokens,
			ContextWindow:       deps.ContextWindow,
			ToolTimeout:         deps.ToolTimeout,
			LLMAPITimeout:       deps.LLMAPITimeout,
			LLMFirstByteTimeout: deps.LLMFirstByteTimeout,
			RootDir:             deps.RootDir,
		}),
	}
}

// SubagentType returns "plan_executor_agent".
func (m *Module) SubagentType() string { return "plan_executor_agent" }

// Run delegates to runner.RunLeaf with builtin.PlanExecutor. Note
// StripDispatchTools=false — the executor must keep the freelance tool
// available to fulfil plan.json steps.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return m.rt.RunLeaf(ctx, runner.Input{
		Def:                  builtin.PlanExecutor,
		Cfg:                  cfg,
		AgentScopeLabel:      "plan_executor_agent",
		SubagentTypeOverride: "plan_executor_agent",
		StripDispatchTools:   false, // executor dispatches via freelance tool
		Hook:                 common.StopOnSubmitResult(),
	})
}
