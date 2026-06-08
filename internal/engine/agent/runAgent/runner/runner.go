// Package runner is the single, data-driven leaf-agent executor that
// replaces the per-type modules under internal/engine/agent/{generic,
// freelancer, plan_agent, plan_executor_agent, plan_design, explore,
// browser_agent}.
//
// Design principle: the LLM↔tool loop is one kernel (internal/engine/loop).
// What differs between "agent types" is data — Profile, AllowedTools,
// MaxTurns, SystemPrompt, terminal hook — all expressible as a single
// agent.AgentDefinition value. RunLeaf takes that value plus a per-call
// Input and assembles loop.Config exactly the way each of the legacy
// modules does today, with no behavioural difference.
//
// New agent types are added by writing a new agent.AgentDefinition
// constant under internal/agent/builtin/ (or a .md frontmatter file);
// no new Go module is required.
package runner

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/legacy/engine_agent_common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// Deps is the dependency surface shared by every leaf-agent run. It mirrors
// the Deps struct each legacy module exposed; emma stamps it once at
// startup. RunLeaf does not mutate any field.
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

// Input is the per-call payload to RunLeaf. It carries the AgentDefinition
// (what kind of agent), the SpawnConfig (per-spawn parameters set by the
// dispatcher), and a few engine-level overrides that callers occasionally
// need to inject.
type Input struct {
	// Def is the agent definition that drives all behavioural choices —
	// Profile, AllowedTools, OutputSchema, SystemPrompt override, MaxTurns
	// default, etc. RunLeaf falls back to a generic worker shape when
	// Def is zero, preserving today's __generic__ behaviour.
	Def agent.AgentDefinition

	// Cfg is the per-spawn config produced by the dispatcher (emma,
	// scheduler tool, agent tool, …). Fields like Prompt, ParentSessionID,
	// ParentOut, InputPaths, etc. are read directly.
	Cfg *agent.SpawnConfig

	// AgentScopeLabel customises the engine-level scope tag the runner
	// stamps on the loop (formerly hard-coded as "generic" / "freelancer"
	// / "plan_agent" etc.). Empty defaults to Def.Name; if that is also
	// empty the fallback "leaf" is used.
	AgentScopeLabel string

	// SubagentTypeOverride pins the SubagentType label propagated into
	// the prompt's <subagent-type> block and the wire subagent.start /
	// .end events. When empty the runner falls back to cfg.SubagentType
	// (legacy generic / freelancer behaviour). The legacy plan_design,
	// explore, plan_agent, plan_executor_agent modules all hard-coded
	// this label to preserve a stable wire identifier even when the
	// dispatcher routes through the SpawnConfig.SubagentType key.
	SubagentTypeOverride string

	// StripDispatchTools, when true, removes the dispatch tools (task /
	// scheduler / freelance) from the tool pool. Strict L3 leaf agents
	// (explore, plan_design, plan_agent, plan_executor_agent, …) set
	// this; the legacy generic and freelancer modules left it false.
	StripDispatchTools bool

	// LoadedSkillsBlock is an optional pre-rendered <loaded-skills> XML
	// snippet the freelancer module prepends to its system prompt after
	// hydrating SKILL.md bodies for cfg.Inputs["candidate_skills"]. Empty
	// for all non-skill-aware agent types.
	LoadedSkillsBlock string

	// OnPoolBuilt is an optional callback fired immediately after the
	// tool pool is constructed (after AllowedTools filter and dispatch
	// strip). The freelancer module uses it to drive its
	// SearchGapDetector, which needs to compare the declared
	// AllowedTools against the pool's actual names. Other modules leave
	// it nil.
	OnPoolBuilt func(pool *tool.ToolPool)

	// Hook overrides Def's default terminal hook. When nil RunLeaf
	// resolves a sensible default from Def (currently always
	// common.StopOnEndTurn). When set, HookFactory is ignored.
	Hook loop.TurnHook

	// HookFactory builds the terminal hook lazily, after RunLeaf has
	// computed the effective maxTurns. Used by the freelancer shim
	// because ContractEnforcerWithLogger needs the resolved maxTurns to
	// time its budget-exhaustion nudge. Ignored when Hook is non-nil.
	HookFactory func(maxTurns int) loop.TurnHook
}

// Runner is the stateless executor. Constructed once at engine start,
// reused for every leaf run.
type Runner struct {
	deps Deps
}

// New constructs a Runner with the given engine-wide deps.
func New(deps Deps) *Runner { return &Runner{deps: deps} }

// RunLeaf executes one leaf-agent end-to-end: build sub-session, resolve
// system prompt, build tool pool, emit subagent.start, run the loop,
// emit subagent.end, return SpawnResult. This is the consolidated body
// of every legacy module.Run.
func (r *Runner) RunLeaf(ctx context.Context, in Input) (*agent.SpawnResult, error) {
	if in.Cfg == nil {
		return nil, errNilCfg
	}
	cfg := in.Cfg
	startTime := time.Now()

	sess, err := common.BuildSubSession(r.deps.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}
	// Pre-create the per-task workspace directory so the LLM's first
	// write doesn't need a recovery mkdir.
	_ = common.EnsureTaskDir(cfg, r.deps.RootDir)

	// Tool pool: per-call AllowedTools whitelist defaults to Def's, which
	// matches the legacy modules' static lists. The generic legacy module
	// passed nil here; nil retains that "no whitelist, blacklist only"
	// behaviour.
	allowed := in.Def.AllowedTools
	pool := common.BuildToolPool(r.deps.Registry, allowed, cfg.AgentType, in.StripDispatchTools)
	if in.OnPoolBuilt != nil {
		in.OnPoolBuilt(pool)
	}

	// Profile: resolve the string field on AgentDefinition to a
	// *prompt.AgentProfile. Empty → WorkerProfile (matches the legacy
	// generic / freelancer / explore / plan_design defaults).
	profile := resolveProfile(in.Def.Profile)

	// SubagentType label: per-call override (set by legacy plan_design,
	// explore, plan_agent shims to pin a stable wire identifier) wins;
	// otherwise the SpawnConfig's SubagentType key passes through.
	subType := in.SubagentTypeOverride
	if subType == "" {
		subType = cfg.SubagentType
	}

	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           profile,
		Builder:           r.deps.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      subType,
		LoadedSkillsBlock: in.LoadedSkillsBlock,
		ContextWindow:     r.deps.ContextWindow,
		Registry:          r.deps.Registry,
	})

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    subType,
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
			Type: types.ContentTypeText, Text: common.SeedPrompt(cfg, r.deps.RootDir),
		}},
	})

	// MaxTurns precedence: per-spawn cfg.MaxTurns > Def.MaxTurns > 10.
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = in.Def.MaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}

	permChecker := common.BuildInheritedChecker(
		common.SessionApprovedTools(r.deps.SessionMgr, cfg.ParentSessionID),
	)

	// Hook resolution precedence: explicit Hook > HookFactory(maxTurns) >
	// default StopOnEndTurn. HookFactory is the freelancer escape hatch
	// — its ContractEnforcerWithLogger needs maxTurns to time the
	// budget-exhaustion nudge.
	hook := in.Hook
	if hook == nil && in.HookFactory != nil {
		hook = in.HookFactory(maxTurns)
	}
	if hook == nil {
		hook = common.StopOnEndTurn()
	}

	scopeLabel := in.AgentScopeLabel
	if scopeLabel == "" {
		scopeLabel = in.Def.Name
	}
	if scopeLabel == "" {
		scopeLabel = "leaf"
	}

	loopRes, err := loop.Run(ctx, &loop.Config{
		Session:             sess,
		SystemPrompt:        sysPrompt,
		Tools:               pool,
		Provider:            r.deps.Provider,
		Compactor:           r.deps.Compactor,
		Retryer:             r.deps.Retryer,
		Logger:              r.deps.Logger,
		MaxTurns:            maxTurns,
		MaxTokens:           r.deps.MaxTokens,
		ContextWindow:       r.deps.ContextWindow,
		ToolTimeout:         r.deps.ToolTimeout,
		LLMAPITimeout:       r.deps.LLMAPITimeout,
		LLMFirstByteTimeout: r.deps.LLMFirstByteTimeout,
		Out:                 cfg.ParentOut,
		AgentID:             sess.ID,
		PermChecker:         permChecker,
		ApprovalFn:          nil, // sub-agents have no approval UI
		AgentScope:          common.BuildAgentScope(cfg, r.deps.RootDir, scopeLabel),
		OnTurnComplete:      hook,
	})
	if err != nil {
		return nil, err
	}

	output := extractText(loopRes.LastMessage)
	terminal := loopRes.Terminal
	usage := loopRes.Usage

	common.EmitSubagentEnd(cfg.ParentOut, common.EndEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentStatus:     StatusFromTerminal(terminal),
		SubagentType:    subType,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Usage:           &usage,
		Terminal:        &terminal,
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
	})

	return common.BuildSpawnResult(sess.ID, sess.ID, output, terminal, usage, loopRes.NumTurns), nil
}

// StatusFromTerminal maps a Terminal.Reason to the wire envelope's
// agent_status string. Exported so the agentrun layer (and tests) can
// reuse the mapping without duplicating it.
func StatusFromTerminal(t types.Terminal) string {
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

// resolveProfile resolves an AgentDefinition.Profile string to a concrete
// *prompt.AgentProfile, defaulting to WorkerProfile so legacy modules with
// no explicit profile (e.g. generic, plan_executor_agent, plan_design)
// keep their behaviour.
func resolveProfile(name string) *prompt.AgentProfile {
	if name == "" {
		return prompt.WorkerProfile
	}
	if p, ok := prompt.GetBuiltInProfiles()[name]; ok && p != nil {
		return p
	}
	return prompt.WorkerProfile
}

// extractText concatenates text blocks from an assistant message into a
// plain string. Mirrors the inline loop the legacy modules used.
func extractText(msg *types.Message) string {
	if msg == nil {
		return ""
	}
	out := ""
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeText {
			out += b.Text
		}
	}
	return out
}
