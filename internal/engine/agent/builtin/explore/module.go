// Package explore runs the read-only exploration sub-agent.
//
// As of the data-driven runner migration this file is a thin shim that
// forwards to engine/runner.Runner.RunLeaf with builtin.Explore.
// Behaviour is unchanged: ExploreProfile, dispatch tools stripped,
// default 10 turns, StopOnEndTurn hook, scope label "explore",
// SubagentType pinned to "explore".
package explore

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

// Module is the explore tier runtime — now a thin runner wrapper.
type Module struct {
	rt *runner.Runner
}

// New constructs an explore Module backed by a runner.Runner.
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

// SubagentType returns "explore" — the registry key the Spawner
// resolves to route exploration spawns at this module.
func (m *Module) SubagentType() string { return "explore" }

// Run delegates to runner.RunLeaf with builtin.Explore. Behaviour is
// identical to the pre-migration module.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return m.rt.RunLeaf(ctx, runner.Input{
		Def:                  builtin.Explore,
		Cfg:                  cfg,
		AgentScopeLabel:      "explore", // preserve legacy scope label
		SubagentTypeOverride: "explore", // pin wire identifier
		StripDispatchTools:   true,      // strict L3 leaf
	})
}
