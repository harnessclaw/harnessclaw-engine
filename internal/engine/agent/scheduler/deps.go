package scheduler

import (
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tool"
)

// Deps is the dependency surface the scheduler (L2 dispatcher) module
// needs from the host engine.
//
// scheduler runs in two modes:
//
//   - react: drives its own LLM loop (like freelancer / plan_executor_agent)
//     with a palette of [freelance, plan_*, meta_*, submit_task_result,
//     read/glob/grep, web_search/tavily_search]. Uses Provider, Registry,
//     Compactor, Retryer, PromptBuilder, MaxTokens, ContextWindow,
//     ToolTimeout, RootDir.
//   - plan: still delegates to the legacy enginesched.Coordinator's plan
//     strategy (plan_agent + plan_executor_agent via msgbus). Uses Coord.
//
// The Spawner field is reserved for a future in-module plan port. Until
// then the plan path round-trips through msgbus → subagent.QueryEngineFactory
// → spawn.Spawner.SpawnSync; we still take Spawner on Deps so emma can
// wire it once and we never have to touch core.go again when the port
// flips the implementation.
type Deps struct {
	// --- react mode (LLM loop) ---

	Provider      provider.Provider
	Registry      *tool.Registry
	SessionMgr    *session.Manager
	Compactor     compact.Compactor
	Retryer       *retry.Retryer
	PromptBuilder *prompt.Builder
	Logger        *zap.Logger

	// MaxTokens is the per-turn output cap forwarded to the provider.
	MaxTokens int

	// ContextWindow is the model's input window in tokens; the loop's
	// compactor gate uses it.
	ContextWindow int

	// ToolTimeout caps wall-clock for one tool call inside the loop.
	// Zero means "no executor-level cap".
	ToolTimeout time.Duration

	// RootDir is the workspace root (e.g. ~/.harnessclaw/workspace).
	// Combined with cfg.RootSessionID it yields the SessionRoot that
	// meta_write / submit_task_result read from ctx.
	RootDir string

	// DefRegistry resolves the "scheduler" AgentDefinition so the react
	// loop can use AgentDefinition.AllowedTools as the BuildToolPool
	// whitelist. Without it the pool falls back to AgentType=Sync's
	// blacklist, which strips `freelance` (the L2→L3 dispatch tool) —
	// the LLM then hallucinates calls and toolexec returns
	// "unknown tool: freelance". nil disables the whitelist (legacy
	// blacklist behaviour preserved for callers that haven't wired this
	// dep).
	DefRegistry *agent.AgentDefinitionRegistry

	// --- plan mode (legacy delegation) ---

	// Coord is the legacy L2 scheduler Coordinator. scheduler.Module
	// forwards plan-mode Run calls to Coord.Run.
	Coord *enginesched.Coordinator

	// Spawner is reserved for the in-module plan strategy port. nil is
	// acceptable today.
	Spawner *spawn.Spawner

	// WorkspaceRoot mirrors RootDir for the plan-mode composeOutput
	// helper (kept as a separate field to preserve the legacy meta.json
	// load path until Stage 8 unifies both modes).
	WorkspaceRoot string
}
