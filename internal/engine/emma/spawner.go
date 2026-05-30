package emma

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/llmcall"
	"harnessclaw-go/internal/engine/prompt"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/engine/toolexec"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// --- spawn.Deps implementation. Engine is the canonical Deps host.

// Logger implements spawn.Deps.
func (e *Engine) Logger() *zap.Logger { return e.logger }

// Provider implements spawn.Deps.
func (e *Engine) Provider() provider.Provider { return e.provider }

// Registry implements spawn.Deps.
func (e *Engine) Registry() *tool.Registry { return e.registry }

// CmdRegistry implements spawn.Deps.
func (e *Engine) CmdRegistry() *command.Registry { return e.cmdRegistry }

// Compactor implements spawn.Deps.
func (e *Engine) Compactor() compact.Compactor { return e.compactor }

// PermChecker implements spawn.Deps.
func (e *Engine) PermChecker() permission.Checker { return e.permChecker }

// SessionMgr implements spawn.Deps.
func (e *Engine) SessionMgr() *session.Manager { return e.sessionMgr }

// StatsRegistry implements spawn.Deps.
func (e *Engine) StatsRegistry() *sessionstats.Registry { return e.statsRegistry }

// DefRegistry implements spawn.Deps.
func (e *Engine) DefRegistry() *agent.AgentDefinitionRegistry { return e.defRegistry }

// SkillReader implements spawn.Deps.
func (e *Engine) SkillReader() *skill.Reader { return e.skillReader }

// PromptBuilder implements spawn.Deps.
func (e *Engine) PromptBuilder() *prompt.Builder { return e.promptBuilder }

// SchedulerCoord implements spawn.Deps.
func (e *Engine) SchedulerCoord() *enginesched.Coordinator { return e.schedulerCoord }

// Retryer implements spawn.Deps.
func (e *Engine) Retryer() *retry.Retryer { return e.retryer }

// SelfSpawner implements spawn.Deps. Returns the engine itself, which
// implements agent.AgentSpawner via SpawnSync below.
func (e *Engine) SelfSpawner() agent.AgentSpawner { return e }

// AgentRegistry implements spawn.Deps.
func (e *Engine) AgentRegistry() *agent.AgentRegistry { return e.agentRegistry }

// MessageBroker implements spawn.Deps.
func (e *Engine) MessageBroker() *agent.MessageBroker { return e.messageBroker }

// SpawnerConfig implements spawn.Deps. Returns a snapshot value so spawn
// never holds a pointer into the engine's mutable config.
func (e *Engine) SpawnerConfig() spawn.SpawnConfig {
	return spawn.SpawnConfig{
		MaxTurns:             e.config.MaxTurns,
		AutoCompactThreshold: e.config.AutoCompactThreshold,
		ToolTimeout:          e.config.ToolTimeout,
		MaxTokens:            e.config.MaxTokens,
		ContextWindow:        e.config.ContextWindow,
		SystemPrompt:         e.config.SystemPrompt,
		ClientTools:          e.config.ClientTools,
		MainAgentDisplayName: e.config.MainAgentDisplayName,
		MaxPlanReplans:       e.config.MaxPlanReplans,
		MaxStepAttempts:      e.config.MaxStepAttempts,
		LLMMaxRetries:        e.config.LLMMaxRetries,
		LLMAPITimeout:        e.config.LLMAPITimeout,
		LLMFirstByteTimeout:  e.config.LLMFirstByteTimeout,
	}
}

// LLMTimeouts implements spawn.Deps.
func (e *Engine) LLMTimeouts() spawn.LLMTimeouts {
	t := llmcall.LLMTimeouts(e.config.LLMAPITimeout, e.config.LLMFirstByteTimeout)
	return spawn.LLMTimeouts{
		API:       t.API,
		FirstByte: t.FirstByte,
	}
}

// CallLLM implements spawn.Deps. Wraps the llmcall package's CallLLM
// free function so spawn can drive one chat round with retries.
func (e *Engine) CallLLM(
	ctx context.Context,
	req *provider.ChatRequest,
	logger *zap.Logger,
	agentID string,
	out, planningOut chan<- types.EngineEvent,
) spawn.LLMCallResult {
	timeouts := llmcall.LLMTimeouts(e.config.LLMAPITimeout, e.config.LLMFirstByteTimeout)
	res := llmcall.CallLLM(ctx, e.provider, req, logger, e.retryer, timeouts, agentID, out, planningOut)
	if res == nil {
		return spawn.LLMCallResult{}
	}
	return spawn.LLMCallResult{
		TextBuf:    res.TextBuf,
		ToolCalls:  res.ToolCalls,
		StopReason: res.StopReason,
		LastUsage:  res.LastUsage,
		Reasoning:  res.Reasoning,
		StreamErr:  res.StreamErr,
	}
}

// NewToolExecutor implements spawn.Deps. Builds a ToolExecutor with the
// engine's standard wiring; spawn calls the returned interface methods.
func (e *Engine) NewToolExecutor(
	pool *tool.ToolPool,
	perm permission.Checker,
	logger *zap.Logger,
	timeout time.Duration,
	approvalFn spawn.ToolApprovalFunc,
) spawn.ToolExecutor {
	te := toolexec.NewToolExecutor(pool, perm, logger, timeout, toolexec.PermissionApprovalFunc(approvalFn))
	if e.statsRegistry != nil {
		te.SetStatsRegistry(e.statsRegistry)
	}
	return te
}

// DispatchToolBatch implements spawn.Deps. Spawn passes back the
// ToolExecutor it received from NewToolExecutor; we down-cast to the
// concrete *ToolExecutor for the dispatch call.
func (e *Engine) DispatchToolBatch(
	ctx context.Context,
	sess *session.Session,
	executor spawn.ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	te, ok := executor.(*toolexec.ToolExecutor)
	if !ok {
		// Defensive: if spawn ever returns a stub the production wiring
		// would never produce, fall through to a fresh executor.
		te = toolexec.NewToolExecutor(pool, e.permChecker, e.logger, e.config.ToolTimeout, nil)
	}
	return e.dispatchToolBatch(ctx, sess, te, pool, toolCalls, out)
}

// BuildAssistantMessage implements spawn.Deps. Wraps the package helper of
// the same name so spawn assembles assistant messages identically.
func (e *Engine) BuildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage, reasoning string) types.Message {
	return BuildAssistantMessage(text, toolCalls, usage, reasoning)
}

// EffectiveContextWindow implements spawn.Deps.
func (e *Engine) EffectiveContextWindow(configured int) int {
	return effectiveContextWindow(configured)
}

// ContextWindow implements spawn.Deps. Returns the engine's effective
// context window for the main loop.
func (e *Engine) ContextWindow() int {
	return e.contextWindow()
}

// GetSkillListingFiltered implements spawn.Deps.
func (e *Engine) GetSkillListingFiltered(allowedSkills map[string]bool) string {
	return e.getSkillListingFiltered(allowedSkills)
}

// GetEnvSnapshot implements spawn.Deps.
func (e *Engine) GetEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	return e.getEnvSnapshot(sessionRoot)
}

// GetSessionApprovedTools implements spawn.Deps. Returns the parent
// session's whitelist of user-approved tools so InheritedChecker can
// pre-approve them for the sub-agent.
func (e *Engine) GetSessionApprovedTools(sessionID string) []string {
	sess := e.sessionMgr.Get(sessionID)
	if sess == nil {
		return nil
	}
	return sess.AllowedTools()
}

// BuildLoadedSkillsBlock implements spawn.Deps.
func (e *Engine) BuildLoadedSkillsBlock(fulls []*skill.SkillFull) string {
	return prompt.BuildLoadedSkillsBlock(fulls)
}

// --- agent.AgentSpawner facade. spawn does the real work.

// SpawnSync implements agent.AgentSpawner. Routes to spawn2 for tier
// modules that have been migrated; falls through to legacy spawn for
// everything else.
//
// Migrated SubagentTypes per stage:
//
//	Stage 4: plan_agent
//	Stage 5: plan_executor_agent, explore, plan, plan_design
//	Stage 6: freelancer
//	Stage 7: scheduler
func (e *Engine) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	if e.spawner2 != nil && useNewSpawn(cfg.SubagentType) {
		return e.spawner2.Sync(ctx, cfg)
	}
	return e.spawner.SpawnSync(ctx, cfg)
}

// useNewSpawn returns true when SubagentType has been migrated to
// spawn2. Updated as stages 4-7 complete.
func useNewSpawn(subagentType string) bool {
	switch subagentType {
	case "plan_agent", "plan_executor_agent", "explore", "plan", "freelancer":
		return true
	// Stage 7 will add: scheduler
	default:
		return false
	}
}

// SpawnAsync implements agent.AsyncSpawner. Delegates to spawn.Spawner.
func (e *Engine) SpawnAsync(ctx context.Context, cfg *agent.SpawnConfig) (string, error) {
	return e.spawner.SpawnAsync(ctx, cfg)
}

// Spawner returns the underlying spawn.Spawner so tests in package emma
// can reach the migrated helpers without exporting them by name.
func (e *Engine) Spawner() *spawn.Spawner { return e.spawner }
