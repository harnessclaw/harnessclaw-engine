// Package freelancer runs the user-skill-driven L3 sub-agent. Capability
// is determined by AllowedSkills loaded at spawn time; ExpectedOutputs
// schema is enforced with retry via ContractEnforcer.
//
// As of the data-driven runner migration this file is a shim that wraps
// engine/runner.Runner.RunLeaf with builtin.Freelancer plus the three
// freelancer-specific touches the generic runner doesn't know about:
//
//   - skill hydration: load SKILL.md bodies for cfg.Inputs["candidate_skills"]
//     and pass the resulting <loaded-skills> block as runner.Input.LoadedSkillsBlock
//   - SearchGapDetector: one-shot SystemNotice when the agent declared
//     web_search / tavily_search but neither backend is registered;
//     wired through runner.Input.OnPoolBuilt
//   - SkillTracker ctx injection: load_skill / unload_skill / etc. read
//     the tracker out of ctx; done before invoking RunLeaf
//   - ContractEnforcerWithLogger as the terminal hook, parameterised
//     by the runner-resolved maxTurns via runner.Input.HookFactory
//   - ResidualFiles post-scan stamped onto the SpawnResult
package freelancer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/builtin"
	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/engine/agent/runAgent/runner"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skills"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// contractRetries is the number of times ContractEnforcer asks the LLM
// to retry a failing submit_task_result before giving up. Matches the
// legacy spawn driver constant.
const contractRetries = 3

// Deps is the dependency surface freelancer needs from the host engine.
// It is a superset of runner.Deps because freelancer additionally needs
// SkillReader / DefRegistry / SearchGapDetector — all freelancer-only
// concerns that don't belong on the shared runner.
type Deps struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger

	// SkillReader resolves candidate_skills names to SkillFull bodies for
	// preload. nil disables candidate preloading.
	SkillReader *skill.Reader

	// DefRegistry lets the module look up the AgentDefinition for the
	// SubagentType. Used by SearchGapDetector to read declared
	// AllowedTools. nil skips the gap check.
	DefRegistry *agent.AgentDefinitionRegistry

	// SearchGapDetector emits a one-shot per-session SystemNotice when
	// declared search capability isn't backed by a registered tool. nil
	// disables the check.
	SearchGapDetector *SearchGapDetector

	MaxTokens           int
	ContextWindow       int
	ToolTimeout         time.Duration
	LLMAPITimeout       time.Duration
	LLMFirstByteTimeout time.Duration
	RootDir             string
}

// Module is the freelancer tier runtime — a thin wrapper around
// runner.Runner with the skill / search-gap / residual-file glue
// retained inline.
type Module struct {
	deps Deps
	rt   *runner.Runner
}

// New constructs a freelancer Module backed by a runner.Runner.
func New(deps Deps) *Module {
	return &Module{
		deps: deps,
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

// SubagentType returns "freelancer".
func (m *Module) SubagentType() string { return "freelancer" }

// Run executes the freelancer L3 loop with skill hydration, search-gap
// detection, contract enforcement, and a residual-file post-scan.
func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	// Step 1: skill hydration. The L2 dispatcher may pass candidate_skills
	// in Inputs; load each SKILL.md, build a tracker, render a
	// <loaded-skills> block to prepend to the system prompt.
	candidates := parseCandidateSkills(cfg.Inputs)
	skillTracker, skillBlock, err := hydrateSkills(m.deps.SkillReader, candidates, cfg.Prompt)
	if err != nil {
		return nil, fmt.Errorf("skill hydration failed for %q: %w", cfg.SubagentType, err)
	}
	if m.deps.Logger != nil && (len(candidates) > 0 || skillBlock != "") {
		m.deps.Logger.Info("freelancer: skill body preloaded",
			zap.Strings("candidate_skills", candidates),
			zap.Int("block_chars", len(skillBlock)),
		)
	}

	// Step 2: inject the SkillTracker so load_skill / unload_skill /
	// list_loaded_skills / search_skill can read & mutate it during loop
	// execution. tool package stores the handle as `any` to avoid
	// importing the engine layer.
	if skillTracker != nil {
		ctx = tool.WithSkillTrackerValue(ctx, skillTracker)
	}

	// Step 3: delegate to the shared runner. ContractEnforcer needs the
	// resolved maxTurns to time its budget-exhaustion nudge, but the
	// runner is the authority on that value — so we hand off via
	// HookFactory and let the runner call us back with the same number
	// it passes to loop.Config.MaxTurns.
	res, err := m.rt.RunLeaf(ctx, runner.Input{
		Def:                  builtin.Freelancer,
		Cfg:                  cfg,
		AgentScopeLabel:      "freelancer",
		SubagentTypeOverride: "freelancer",
		StripDispatchTools:   true,
		LoadedSkillsBlock:    skillBlock,
		// SearchGapDetector compares declared AllowedTools against the
		// resolved tool pool; the runner fires this callback right
		// after the pool is built.
		OnPoolBuilt: m.searchGapCallback(ctx, cfg),
		HookFactory: func(mt int) loop.TurnHook {
			return common.ContractEnforcerWithLogger(
				cfg.ExpectedOutputs,
				contractRetries,
				mt,
				m.deps.Logger,
			)
		},
	})
	if err != nil {
		return nil, err
	}

	// Step 4: residual-file post-scan stays freelancer-specific.
	res.ResidualFiles = common.ScanResidualFiles(cfg, m.deps.RootDir)
	return res, nil
}

// searchGapCallback returns a closure suitable for runner.Input.OnPoolBuilt
// that fires the SearchGapDetector once the runner has constructed the
// tool pool. nil-safe on every field; returns nil to disable the check.
func (m *Module) searchGapCallback(ctx context.Context, cfg *agent.SpawnConfig) func(*tool.ToolPool) {
	if m.deps.SearchGapDetector == nil {
		return nil
	}
	return func(pool *tool.ToolPool) {
		var declared []string
		if m.deps.DefRegistry != nil {
			if def := m.deps.DefRegistry.Get(cfg.SubagentType); def != nil {
				declared = def.AllowedTools
			}
		}
		m.deps.SearchGapDetector.CheckAndEmit(
			ctx, cfg.ParentSessionID, cfg.SubagentType,
			declared, pool.Names(),
			func(ctx context.Context, ev types.EngineEvent) error {
				if cfg.ParentOut == nil {
					return fmt.Errorf("parent out channel is nil")
				}
				select {
				case cfg.ParentOut <- ev:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		)
	}
}
