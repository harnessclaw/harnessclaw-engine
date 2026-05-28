package queryloop

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
)

// PromptConfig is the engine-side configuration snapshot that the prompt-
// building helpers consume. Runner reads through Deps.PromptConfig() so
// queryloop never imports engine and never touches engine's mutable
// QueryEngineConfig struct directly.
type PromptConfig struct {
	// SystemPrompt is the static fallback used when promptBuilder fails
	// or is not configured.
	SystemPrompt string
	// ContextWindow is the effective context-window budget the prompt
	// builder reports to PromptContext. Should be the resolved value
	// (engine.contextWindow() output), not raw config.
	ContextWindow int
}

// LoopConfig is the engine-side configuration snapshot consumed by the
// main query loop. Returned by value so Runner never holds a pointer
// into the engine's mutable config.
type LoopConfig struct {
	MaxTurns              int
	MainAgentMaxTurns     int
	MaxTokens             int
	AutoCompactThreshold  float64
	ToolTimeout           time.Duration
	ClientTools           bool
	MainAgentAllowedTools []string
	LLMAPITimeout         time.Duration
	LLMFirstByteTimeout   time.Duration
}

// Deps is the dependency surface QueryEngine implements so queryloop.Runner
// can do its work.
type Deps interface {
	Logger() *zap.Logger
	EventBus() *event.Bus

	SessionMgr() *session.Manager

	// Sub-service handles
	Spawner() *spawn.Spawner

	// Cancellation registry — Runner manages per-session cancel context.
	RegisterCancel(sid string, cancel context.CancelFunc)
	DeregisterCancel(sid string)

	// Prompt-building accessors (added in Task 5.4b).
	PromptBuilder() *prompt.Builder
	PromptProfile() *prompt.AgentProfile
	SkillReader() *skill.Reader
	DefRegistry() *agent.AgentDefinitionRegistry
	StatsRegistry() *sessionstats.Registry
	CmdRegistry() *command.Registry
	Registry() *tool.Registry

	// PromptConfig is a snapshot of the engine-side configuration fields
	// the prompt builders read. Returns a value so Runner never holds a
	// pointer into engine state.
	PromptConfig() PromptConfig

	// --- Added in 5.4e for runQueryLoop ---

	// LoopConfig is a snapshot of the loop-relevant config fields.
	LoopConfig() LoopConfig

	// ContextWindow returns the engine's effective context window (operator
	// value or 200k fallback) so Runner doesn't need to duplicate the
	// resolution rule.
	ContextWindow() int

	// Provider returns the LLM provider driving Chat() calls.
	Provider() provider.Provider

	// Retryer drives the per-LLM-call retry loop with backoff + jitter.
	Retryer() *retry.Retryer

	// Compactor decides when to auto-compact and produces the compacted
	// message history. May be nil — Runner gates on nil before calling.
	Compactor() compact.Compactor

	// PermChecker is the permission gate handed to the ToolExecutor.
	PermChecker() permission.Checker

	// AgentRegistry, when non-nil, lets the loop check whether any
	// async sub-agents from this session are still running.
	AgentRegistry() *agent.AgentRegistry

	// MessageBroker, when non-nil, lets the loop register a mailbox for
	// inbound async-agent notifications.
	MessageBroker() *agent.MessageBroker
}

// Runner drives one user turn. Constructed once per engine, reused across
// ProcessMessage calls. Internal state is per-session and lives in the
// session, not on Runner.
type Runner struct {
	deps Deps

	// skillListingOnce gates the lazy computation of skillListing; the
	// listing is reused across every turn for the lifetime of the engine.
	skillListingOnce sync.Once
	skillListing     string
}

// NewRunner constructs a Runner backed by the given Deps. Deps must remain
// valid for the Runner's lifetime — typically Deps is the parent QueryEngine.
func NewRunner(deps Deps) *Runner {
	return &Runner{deps: deps}
}
