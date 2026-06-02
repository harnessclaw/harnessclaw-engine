package emma

import (
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
)

// Config holds tunables for the emma engine's query loop.
//
// This is the post-flattening replacement for the old
// engine.QueryEngineConfig. Field names and semantics are preserved 1:1
// so existing wiring code transitions verbatim.
type Config struct {
	MaxTurns             int
	AutoCompactThreshold float64
	ToolTimeout          time.Duration
	// MaxTokens caps the response length on each Chat() call (passed
	// through as ChatRequest.MaxTokens). 0 = let the bifrost adapter
	// fall back to its agent/endpoint-resolved default. Sourced from
	// cfg.Agent.MaxTokens.
	MaxTokens int
	// ContextWindow is the conversation-level token budget the
	// compactor watches; auto-compact fires when accumulated message
	// tokens exceed ContextWindow × AutoCompactThreshold. Sourced
	// from cfg.Agent.ContextWindow with a 200_000 fallback for
	// modern provider defaults (anthropic / openai / gemini 200k).
	// Distinct from MaxTokens — previously both lived in MaxTokens,
	// which mixed "response cap" and "context budget" semantics.
	ContextWindow int
	SystemPrompt  string
	// ClientTools enables client-side tool execution mode.
	// When true, tool calls are sent to the client via tool_call events
	// instead of being executed server-side.
	ClientTools bool

	// MainAgentProfile is the prompt profile used for the user-facing main
	// agent (the one invoked via ProcessMessage). Sub-agents resolve their
	// own profile via SpawnConfig.SubagentType — this field is for the
	// non-spawn path only. When nil, falls back to WorkerProfile.
	MainAgentProfile *prompt.AgentProfile

	// MainAgentDisplayName is the friendly leader name interpolated into
	// worker identity prompts (e.g., "你叫小林，是 emma 团队的搭档"). Empty
	// disables the substitution and keeps a generic worker identity.
	MainAgentDisplayName string

	// MainAgentAllowedTools restricts the tools advertised to the main
	// agent's LLM. Empty means no restriction (all enabled tools visible).
	MainAgentAllowedTools []string

	// MainAgentMaxTurns overrides MaxTurns for the user-facing main agent
	// loop only. When > 0, the main loop terminates after this many turns;
	// sub-agents continue to derive their cap from MaxTurns.
	MainAgentMaxTurns int

	// MaxPlanReplans bounds PlanCoordinator re-plan attempts. Zero means
	// "use defaultMaxPlanReplans" (3).
	MaxPlanReplans int

	// MaxStepAttempts bounds Scheduler retries per transient step
	// failure. Zero means "use defaultStepMaxAttempts" (3).
	MaxStepAttempts int

	// LLMMaxRetries overrides retry.DefaultConfig().MaxRetries when > 0.
	LLMMaxRetries int

	// LLMAPITimeout caps total wall-clock for ONE LLM call.
	LLMAPITimeout time.Duration

	// LLMFirstByteTimeout caps how long we wait between Chat() returning
	// and the FIRST stream chunk arriving.
	LLMFirstByteTimeout time.Duration

	// DisableStepDecisionGate, when true, suppresses the user-decision
	// prompt that the Scheduler / PlanCoordinator would otherwise emit
	// after a step / plan-level failure.
	DisableStepDecisionGate bool

	// DefRegistry, when non-nil, enables @-mention parsing for routing
	// user messages to specialized agents.
	DefRegistry *agent.AgentDefinitionRegistry

	// SkillReader, when non-nil, enables runtime skill discovery for
	// search_skill / load_skill tools.
	SkillReader *skill.Reader

	// BrowserAgent holds the browser-agent tool family configuration,
	// forwarded to the browser-agent module so it can construct its
	// SkillProvider.
	BrowserAgent config.BrowserAgentConfig

	// StatsRegistry, when non-nil, lets the engine attribute LLM /
	// sub-agent / tool activity to the correct Tracker.
	StatsRegistry *sessionstats.Registry
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          120 * time.Second,
		MaxTokens:            16384,
		ContextWindow:        200000,
		SystemPrompt:         "You are a helpful assistant.",
		ClientTools:          true,
		MaxPlanReplans:       3,
		MaxStepAttempts:      3,
		LLMAPITimeout:        600 * time.Second,
		LLMFirstByteTimeout:  120 * time.Second,
	}
}

// retryConfigFromCfg builds a *retry.Config from the engine config,
// applying package-level defaults for fields the engine doesn't override.
func retryConfigFromCfg(cfg Config) *retry.Config {
	c := retry.DefaultConfig()
	if cfg.LLMMaxRetries > 0 {
		c.MaxRetries = cfg.LLMMaxRetries
	}
	return c
}

// effectiveContextWindow resolves the operator value with the production
// fallback (200k aligned with modern provider defaults).
func effectiveContextWindow(configured int) int {
	if configured > 0 {
		return configured
	}
	return 200000
}
