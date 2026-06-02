// Package freelancer runs the user-skill-driven L3 sub-agent. Capability
// is determined by AllowedSkills loaded at spawn time; ExpectedOutputs
// schema is enforced with retry via ContractEnforcer.
//
// Freelancer differs from thin tier modules (plan_agent / explore / ...):
//
//   - skill hydration: the L2 dispatcher may pass `candidate_skills` in
//     SpawnConfig.Inputs; freelancer loads each SKILL.md, preloads it
//     into a SkillTracker, and prepends a <loaded-skills> XML block to
//     the system prompt.
//   - SearchGapDetector: emits a one-shot SystemNotice to the user when
//     the agent declared web_search / tavily_search capability but
//     neither backend is registered at runtime.
//   - ContractEnforcer: per-spawn ExpectedOutputs schema is validated
//     with up to 3 retries before the loop terminates.
package freelancer

import (
	"context"
	"fmt"
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
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// contractRetries is the number of times ContractEnforcer asks the LLM
// to retry a failing submit_task_result before giving up. Matches the
// legacy spawn driver constant.
const contractRetries = 3

// Deps is the dependency surface freelancer needs from the host engine.
//
// MaxTokens and ContextWindow live on Deps (not SpawnConfig) because
// they are engine-wide knobs sourced from the parent engine.Config, not
// per-spawn overrides. emma.NewSpawner stamps them once at startup.
//
// SkillReader is freelancer-specific — it backs the hydrateSkills step
// that loads SKILL.md content for cfg.Inputs["candidate_skills"].
type Deps struct {
	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger

	// SkillReader resolves candidate_skills names to SkillFull bodies for
	// preload. nil disables candidate preloading — freelancer can still
	// load skills at runtime via search_skill / load_skill tools.
	SkillReader *skill.Reader

	// DefRegistry lets the module look up the AgentDefinition for the
	// SubagentType. Used by SearchGapDetector to read declared
	// AllowedTools. nil skips the gap check.
	DefRegistry *agent.AgentDefinitionRegistry

	// SearchGapDetector emits a one-shot per-session SystemNotice when
	// declared search capability isn't backed by a registered tool. nil
	// disables the check.
	SearchGapDetector *SearchGapDetector

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

	// RootDir is the workspace root (e.g. ~/.harnessclaw/workspace).
	// Combined with cfg.RootSessionID it yields the SessionRoot that
	// meta_write / submit_task_result read from ctx.
	RootDir string
}

// Module is the freelancer tier runtime.
type Module struct {
	deps Deps
}

// New constructs a freelancer Module.
func New(deps Deps) *Module {
	return &Module{deps: deps}
}

// SubagentType returns "freelancer" — the typed key the Spawner resolves
// to route freelancer spawns at this module.
func (m *Module) SubagentType() string { return "freelancer" }

// Run executes the freelancer L3 LLM loop:
//  1. build sub-session
//  2. hydrate skills (load candidate_skills SKILL.md, build tracker)
//  3. build tool pool (dispatch stripped — freelancer is a strict leaf)
//  4. render FreelancerProfile prompt with <loaded-skills> block
//  5. inject SkillTracker into ctx so load_skill / unload_skill /
//     list_loaded_skills / search_skill can mutate it
//  6. emit subagent.start
//  7. SearchGapDetector check (one-shot SystemNotice on capability gap)
//  8. run loop with ContractEnforcer(ExpectedOutputs, 3)
//  9. emit subagent.end
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

	// Step 1: Skill hydration. The L2 dispatcher may pass candidate_skills
	// in Inputs; load each SKILL.md, build a tracker, render a
	// <loaded-skills> block to prepend to the system prompt.
	candidates := parseCandidateSkills(cfg.Inputs)
	skillTracker, skillBlock, err := hydrateSkills(m.deps.SkillReader, candidates, cfg.Prompt)
	if err != nil {
		return nil, fmt.Errorf("skill hydration failed for %q: %w", cfg.SubagentType, err)
	}

	// freelancer is L3 leaf: strip dispatch tools so it cannot recursively
	// spawn. No per-spawn AllowedTools whitelist: SpawnConfig only
	// carries AllowedSkills (skill scoping), not a tool name allowlist.
	// AgentType blacklist is still applied inside BuildToolPool.
	pool := common.BuildToolPool(m.deps.Registry, nil, cfg.AgentType, true)

	sysPrompt := common.BuildSubAgentPrompt(common.PromptArgs{
		Ctx:               ctx,
		Session:           sess,
		Profile:           prompt.FreelancerProfile,
		Builder:           m.deps.PromptBuilder,
		WorkerDisplayName: cfg.Name,
		SubagentType:      "freelancer",
		LoadedSkillsBlock: skillBlock,
		ContextWindow:     m.deps.ContextWindow,
		Registry:          m.deps.Registry,
	})

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    "freelancer",
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
		ParentStepID:    cfg.ParentStepID,
	})

	// Search-tool capability gap — emit a one-shot CardSystem notice
	// to the user when this sub-agent declares search capability but
	// neither web_search nor tavily_search is registered at runtime.
	// nil-safe on detector, registry, or ParentOut.
	if m.deps.SearchGapDetector != nil {
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

	ctx = common.WithSubAgentStats(ctx, sess.ID, sess.ID,
		cfg.ParentSessionID, cfg.RootSessionID)

	// Inject the SkillTracker so load_skill / unload_skill /
	// list_loaded_skills / search_skill can read & mutate it during loop
	// execution. tool package stores the handle as `any` to avoid
	// importing the engine layer.
	if skillTracker != nil {
		ctx = tool.WithSkillTrackerValue(ctx, skillTracker)
	}

	// Seed session with the prompt as the first user message. Prepend
	// a workspace prelude (task_dir + task_id navigation hint) so the
	// LLM doesn't have to call bash pwd just to find where to write
	// the output file — previously freelancer produced files into
	// random cwd paths because nothing told it where its task_dir was.
	sess.AddMessage(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText, Text: common.SeedPrompt(cfg, m.deps.RootDir),
		}},
	})

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		// freelancer typically needs more turns than fixed-role L3
		// agents: it discovers skills, loads them, then executes the
		// underlying task.
		maxTurns = 25
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
		Out:            cfg.ParentOut,
		AgentID:        sess.ID,
		PermChecker:    permChecker,
		ApprovalFn:     nil, // sub-agents have no approval UI
		AgentScope:     common.BuildAgentScope(cfg, m.deps.RootDir, "freelancer"),
		OnTurnComplete: common.ContractEnforcer(cfg.ExpectedOutputs, contractRetries),
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
		SubagentType:    "freelancer",
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
