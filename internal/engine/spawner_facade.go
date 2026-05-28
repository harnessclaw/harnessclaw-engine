package engine

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
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// workspaceRootDir is a package-local alias of workspace.DefaultRootDir
// so engine internals can reach the shared default without each call site
// re-importing the workspace package. Duplicates the helper that used to
// live in subagent.go; spawn now has its own copy under internal/engine/spawn.
func workspaceRootDir() string {
	return workspace.DefaultRootDir()
}

// --- spawn.Deps implementation. QueryEngine is the canonical Deps host.

// Logger implements spawn.Deps.
func (qe *QueryEngine) Logger() *zap.Logger { return qe.logger }

// Provider implements spawn.Deps.
func (qe *QueryEngine) Provider() provider.Provider { return qe.provider }

// Registry implements spawn.Deps.
func (qe *QueryEngine) Registry() *tool.Registry { return qe.registry }

// CmdRegistry implements spawn.Deps.
func (qe *QueryEngine) CmdRegistry() *command.Registry { return qe.cmdRegistry }

// Compactor implements spawn.Deps.
func (qe *QueryEngine) Compactor() compact.Compactor { return qe.compactor }

// PermChecker implements spawn.Deps.
func (qe *QueryEngine) PermChecker() permission.Checker { return qe.permChecker }

// EventBus implements spawn.Deps.
func (qe *QueryEngine) EventBus() *event.Bus { return qe.eventBus }

// SessionMgr implements spawn.Deps.
func (qe *QueryEngine) SessionMgr() *session.Manager { return qe.sessionMgr }

// StatsRegistry implements spawn.Deps.
func (qe *QueryEngine) StatsRegistry() *sessionstats.Registry { return qe.statsRegistry }

// DefRegistry implements spawn.Deps.
func (qe *QueryEngine) DefRegistry() *agent.AgentDefinitionRegistry { return qe.defRegistry }

// SkillReader implements spawn.Deps.
func (qe *QueryEngine) SkillReader() *skill.Reader { return qe.skillReader }

// PromptBuilder implements spawn.Deps.
func (qe *QueryEngine) PromptBuilder() *prompt.Builder { return qe.promptBuilder }

// SchedulerCoord implements spawn.Deps.
func (qe *QueryEngine) SchedulerCoord() *enginesched.Coordinator { return qe.schedulerCoord }

// Retryer implements spawn.Deps.
func (qe *QueryEngine) Retryer() *retry.Retryer { return qe.retryer }

// SelfSpawner implements spawn.Deps. Returns the parent QE which itself
// implements agent.AgentSpawner (via SpawnSync delegation below).
func (qe *QueryEngine) SelfSpawner() agent.AgentSpawner { return qe }

// AgentRegistry implements spawn.Deps.
func (qe *QueryEngine) AgentRegistry() *agent.AgentRegistry { return qe.agentRegistry }

// MessageBroker implements spawn.Deps.
func (qe *QueryEngine) MessageBroker() *agent.MessageBroker { return qe.messageBroker }

// SpawnerConfig implements spawn.Deps. Returns a snapshot value so spawn
// never holds a pointer into QE's mutable config. Named distinctly from
// Config() (which returns QueryEngineConfig).
func (qe *QueryEngine) SpawnerConfig() spawn.SpawnConfig {
	return spawn.SpawnConfig{
		MaxTurns:             qe.config.MaxTurns,
		AutoCompactThreshold: qe.config.AutoCompactThreshold,
		ToolTimeout:          qe.config.ToolTimeout,
		MaxTokens:            qe.config.MaxTokens,
		ContextWindow:        qe.config.ContextWindow,
		SystemPrompt:         qe.config.SystemPrompt,
		ClientTools:          qe.config.ClientTools,
		MainAgentDisplayName: qe.config.MainAgentDisplayName,
		MaxPlanReplans:       qe.config.MaxPlanReplans,
		MaxStepAttempts:      qe.config.MaxStepAttempts,
		LLMMaxRetries:        qe.config.LLMMaxRetries,
		LLMAPITimeout:        qe.config.LLMAPITimeout,
		LLMFirstByteTimeout:  qe.config.LLMFirstByteTimeout,
	}
}

// LLMTimeouts implements spawn.Deps.
func (qe *QueryEngine) LLMTimeouts() spawn.LLMTimeouts {
	t := llmcall.LLMTimeouts(qe.config.LLMAPITimeout, qe.config.LLMFirstByteTimeout)
	return spawn.LLMTimeouts{
		API:       t.API,
		FirstByte: t.FirstByte,
	}
}

// CallLLM implements spawn.Deps. Wraps the llmcall package's CallLLM
// free function so spawn can drive one chat round with retries.
func (qe *QueryEngine) CallLLM(
	ctx context.Context,
	req *provider.ChatRequest,
	logger *zap.Logger,
	agentID string,
	out, planningOut chan<- types.EngineEvent,
) spawn.LLMCallResult {
	timeouts := llmcall.LLMTimeouts(qe.config.LLMAPITimeout, qe.config.LLMFirstByteTimeout)
	res := llmcall.CallLLM(ctx, qe.provider, req, logger, qe.retryer, timeouts, agentID, out, planningOut)
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
func (qe *QueryEngine) NewToolExecutor(
	pool *tool.ToolPool,
	perm permission.Checker,
	logger *zap.Logger,
	timeout time.Duration,
	approvalFn spawn.ToolApprovalFunc,
) spawn.ToolExecutor {
	te := NewToolExecutor(pool, perm, logger, timeout, PermissionApprovalFunc(approvalFn))
	if qe.statsRegistry != nil {
		te.SetStatsRegistry(qe.statsRegistry)
	}
	return te
}

// DispatchToolBatch implements spawn.Deps. Spawn passes back the
// ToolExecutor it received from NewToolExecutor; we down-cast to the
// concrete *ToolExecutor for the dispatch call.
func (qe *QueryEngine) DispatchToolBatch(
	ctx context.Context,
	executor spawn.ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	te, ok := executor.(*ToolExecutor)
	if !ok {
		// Defensive: if spawn ever returns a stub the production wiring
		// would never produce, fall through to a fresh executor so the
		// loop doesn't crash mid-tool-call. This branch is unreachable
		// from real callers.
		te = NewToolExecutor(pool, qe.permChecker, qe.logger, qe.config.ToolTimeout, nil)
	}
	return qe.dispatchToolBatch(ctx, te, pool, toolCalls, out)
}

// BuildAssistantMessage implements spawn.Deps. Wraps the engine helper
// of the same name so spawn assembles assistant messages identically.
func (qe *QueryEngine) BuildAssistantMessage(text string, toolCalls []types.ToolCall, usage *types.Usage, reasoning string) types.Message {
	return buildAssistantMessage(text, toolCalls, usage, reasoning)
}

// EffectiveContextWindow implements spawn.Deps.
func (qe *QueryEngine) EffectiveContextWindow(configured int) int {
	return effectiveContextWindow(configured)
}

// ContextWindow implements spawn.Deps. Returns the engine's effective
// context window for the main loop.
func (qe *QueryEngine) ContextWindow() int {
	return qe.contextWindow()
}

// GetSkillListingFiltered implements spawn.Deps.
func (qe *QueryEngine) GetSkillListingFiltered(allowedSkills map[string]bool) string {
	return qe.getSkillListingFiltered(allowedSkills)
}

// GetEnvSnapshot implements spawn.Deps.
func (qe *QueryEngine) GetEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	return qe.getEnvSnapshot(sessionRoot)
}

// GetSessionApprovedTools implements spawn.Deps. Returns the parent
// session's whitelist of user-approved tools so InheritedChecker can
// pre-approve them for the sub-agent.
func (qe *QueryEngine) GetSessionApprovedTools(sessionID string) []string {
	qe.sessionAllowMu.RLock()
	defer qe.sessionAllowMu.RUnlock()
	tools, ok := qe.sessionAllowTools[sessionID]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(tools))
	for k := range tools {
		result = append(result, k)
	}
	return result
}

// BuildLoadedSkillsBlock implements spawn.Deps.
func (qe *QueryEngine) BuildLoadedSkillsBlock(fulls []*skill.SkillFull) string {
	return buildLoadedSkillsBlock(fulls)
}

// --- agent.AgentSpawner facade. spawn does the real work.

// SpawnSync implements agent.AgentSpawner. Delegates to spawn.Spawner.
func (qe *QueryEngine) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return qe.spawner.SpawnSync(ctx, cfg)
}

// SpawnAsync implements agent.AsyncSpawner. Delegates to spawn.Spawner.
func (qe *QueryEngine) SpawnAsync(ctx context.Context, cfg *agent.SpawnConfig) (string, error) {
	return qe.spawner.SpawnAsync(ctx, cfg)
}

// Spawner returns the underlying spawn.Spawner so tests in package engine
// can reach the migrated helpers (BuildSubAgentSystemPrompt and the like).
func (qe *QueryEngine) Spawner() *spawn.Spawner { return qe.spawner }

// session is imported above to satisfy the spawn.Deps signature; this
// no-op keeps `goimports` from dropping it when other symbols in the
// file don't reference the package directly.
var _ = session.StateActive
