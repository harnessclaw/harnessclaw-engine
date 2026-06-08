// Package generic is the fallback tier module for SubagentTypes that
// have no specialised package. Custom YAML agents and ad-hoc types
// route here.
//
// As of the data-driven runner migration this file is a 30-line shim:
// it forwards to engine/runner.Runner.RunLeaf with the builtin.Generic
// AgentDefinition. The original ~220-line module body now lives in
// internal/engine/runner/runner.go and is shared by every leaf-tier
// agent. Behaviour is unchanged — Profile=WorkerProfile, no AllowedTools
// whitelist, MaxTurns default 10, StopOnEndTurn hook.
package generic

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

// Deps mirrors the original Deps struct so existing wiring in
// emma.NewSpawner compiles untouched. Internally it is forwarded to
// runner.Deps.
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

// Module is the generic-tier sub-agent runtime. After the migration its
// only job is to hold a *runner.Runner and forward Run calls.
type Module struct {
	rt *runner.Runner
}

// New constructs a generic Module backed by a runner.Runner. The Deps
// shape is preserved 1:1 so emma's spawner wiring is untouched.
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

// SubagentType returns "__generic__" — the conventional fallback key.
// Preserved verbatim from the legacy module.
func (m *Module) SubagentType() string { return "__generic__" }

// Run delegates to runner.RunLeaf with builtin.Generic. Behaviour is
// identical to the pre-migration module.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return m.rt.RunLeaf(ctx, runner.Input{
		Def:             builtin.Generic,
		Cfg:             cfg,
		AgentScopeLabel: "generic", // preserve legacy scope label
	})
}
