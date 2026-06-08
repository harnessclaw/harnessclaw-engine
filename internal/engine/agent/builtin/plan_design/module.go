// Package plan_design runs the design / methodology planning sub-agent
// (the user-facing "Plan" role; NOT the plan-mode plan_agent).
//
// As of the data-driven runner migration this file is a thin shim that
// forwards to engine/runner.Runner.RunLeaf with builtin.PlanDesign.
// Behaviour is unchanged: PlanProfile, dispatch tools stripped, default
// 15 turns, StopOnEndTurn hook, scope label "plan_design",
// SubagentType pinned to "plan".
package plan_design

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/builtin"
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

// Module is the plan_design tier runtime — now a thin runner wrapper.
type Module struct {
	rt *runner.Runner
}

// New constructs a plan_design Module backed by a runner.Runner.
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

// SubagentType returns "plan" — the registry key the Spawner resolves
// to route design / methodology planning spawns at this module. Note
// the Go package is plan_design (since "plan" is reserved for the
// plan-mode plan_agent), but the user-facing roster key is "plan".
func (m *Module) SubagentType() string { return "plan" }

// Run delegates to runner.RunLeaf with builtin.PlanDesign. Behaviour is
// identical to the pre-migration module.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return m.rt.RunLeaf(ctx, runner.Input{
		Def:                  builtin.PlanDesign,
		Cfg:                  cfg,
		AgentScopeLabel:      "plan_design", // preserve legacy scope label
		SubagentTypeOverride: "plan",        // pin wire identifier
		StripDispatchTools:   true,          // strict L3 leaf
	})
}
