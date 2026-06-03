// Package config loads and provides application configuration via Viper.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"

	"harnessclaw-go/internal/provider/bifrost"
)

// Validate checks that the loaded configuration contains valid values.
// Validate checks "hard" errors that prevent the server from
// running at all (port, engine/permission/health knobs). LLM
// content errors (bad provider/endpoint/chain entries) are NOT
// checked here — call SanitizeLLM to drop them with WARN logs
// instead so the rest of the config still starts up.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port)
	}
	if c.Agent.MaxTurns < 1 {
		return fmt.Errorf("agent.max_turns must be at least 1, got %d", c.Agent.MaxTurns)
	}
	if c.Agent.MaxToolCalls < 0 {
		return fmt.Errorf("agent.max_tool_calls must be non-negative (0 = unlimited), got %d", c.Agent.MaxToolCalls)
	}
	if c.Agent.ThinkingIntensity != "" {
		switch c.Agent.ThinkingIntensity {
		case ThinkingIntensityLow, ThinkingIntensityMedium, ThinkingIntensityHigh:
		default:
			return fmt.Errorf("agent.thinking_intensity must be one of low/medium/high (or empty), got %q", c.Agent.ThinkingIntensity)
		}
	}
	if c.Engine.AutoCompactThreshold < 0 || c.Engine.AutoCompactThreshold > 1.0 {
		return fmt.Errorf("engine.auto_compact_threshold must be between 0 and 1, got %f", c.Engine.AutoCompactThreshold)
	}
	validModes := map[string]bool{
		"default": true, "plan": true, "bypass": true, "acceptEdits": true, "dontAsk": true,
	}
	if c.Permission.Mode != "" && !validModes[c.Permission.Mode] {
		return fmt.Errorf("permission.mode must be one of default/plan/bypass/acceptEdits/dontAsk, got %q", c.Permission.Mode)
	}
	if c.LLM.MaxRetries < 0 {
		return fmt.Errorf("llm.max_retries must be non-negative, got %d", c.LLM.MaxRetries)
	}
	if c.LLM.FirstByteTimeout < 0 {
		return fmt.Errorf("llm.first_byte_timeout must be non-negative, got %s", c.LLM.FirstByteTimeout)
	}
	if c.LLM.Health.CooldownBase < 0 {
		return fmt.Errorf("llm.health.cooldown_base must be non-negative, got %s", c.LLM.Health.CooldownBase)
	}
	if c.LLM.Health.CooldownMax < 0 {
		return fmt.Errorf("llm.health.cooldown_max must be non-negative, got %s", c.LLM.Health.CooldownMax)
	}
	if c.LLM.Health.CooldownFactor < 0 {
		return fmt.Errorf("llm.health.cooldown_factor must be non-negative, got %d", c.LLM.Health.CooldownFactor)
	}
	if c.LLM.Health.PrimaryBudget < 0 {
		return fmt.Errorf("llm.health.primary_budget must be non-negative, got %s", c.LLM.Health.PrimaryBudget)
	}
	if c.LLM.Health.LastHealthyBudget < 0 {
		return fmt.Errorf("llm.health.last_healthy_budget must be non-negative, got %s", c.LLM.Health.LastHealthyBudget)
	}
	if c.LLM.Health.ProbeBudget < 0 {
		return fmt.Errorf("llm.health.probe_budget must be non-negative, got %s", c.LLM.Health.ProbeBudget)
	}
	return nil
}

// SanitizeLLM drops malformed providers / endpoints / chain entries
// from LLM config, logging each removal at WARN. Run AFTER Validate
// — Validate catches hard errors that must prevent startup;
// SanitizeLLM tolerates content-level errors so the server can boot
// with whatever IS valid.
//
// Rules:
//
//   - provider name contains ':' or '.' → drop the provider
//   - provider.type empty or not in bifrost allowed list → drop
//   - endpoint name contains ':' → drop the endpoint (other endpoints
//     under the same provider survive)
//   - endpoint.model empty → drop the endpoint
//   - chain entry parse error / unknown provider / unknown endpoint
//     → drop from chain
//
// The chain pass runs AFTER provider/endpoint sanitisation, so any
// chain entry pointing at a just-dropped provider/endpoint is also
// dropped in the same call.
//
// Mutates c in place. Logger may be nil (drops happen silently —
// fine for tests).
func (c *Config) SanitizeLLM(logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}

	// Pass 1: providers (and their endpoints).
	for name, p := range c.LLM.Providers {
		if strings.ContainsAny(name, ":.") {
			logger.Warn("config sanitize: dropping provider — name contains ':' or '.'",
				zap.String("provider", name))
			delete(c.LLM.Providers, name)
			continue
		}
		if p.Type == "" {
			logger.Warn("config sanitize: dropping provider — type field is empty",
				zap.String("provider", name),
				zap.Strings("allowed", bifrost.AllowedTypeNames()))
			delete(c.LLM.Providers, name)
			continue
		}
		if _, ok := bifrost.ProviderTypeOf(p.Type); !ok {
			logger.Warn("config sanitize: dropping provider — unknown type",
				zap.String("provider", name),
				zap.String("type", p.Type),
				zap.Strings("allowed", bifrost.AllowedTypeNames()))
			delete(c.LLM.Providers, name)
			continue
		}
		// Pass 1.5: endpoints under this provider.
		for eName, e := range p.Endpoints {
			if strings.Contains(eName, ":") {
				logger.Warn("config sanitize: dropping endpoint — name contains ':' (canonical chain ref separator)",
					zap.String("provider", name),
					zap.String("endpoint", eName))
				delete(p.Endpoints, eName)
				continue
			}
			if e.Model == "" {
				logger.Warn("config sanitize: dropping endpoint — model field is empty",
					zap.String("provider", name),
					zap.String("endpoint", eName))
				delete(p.Endpoints, eName)
				continue
			}
		}
		// Re-store provider config (Endpoints map is reference type
		// so deletions inside the loop have already taken effect,
		// but be explicit for clarity).
		c.LLM.Providers[name] = p
	}

	// Pass 2: agent.primary (must resolve to an existing endpoint;
	// if not, clear it — primary empty enters degraded mode).
	if c.Agent.Primary != "" {
		if !c.endpointExists(c.Agent.Primary) {
			logger.Warn("config sanitize: clearing agent.primary — unresolved reference",
				zap.String("primary", c.Agent.Primary))
			c.Agent.Primary = ""
		}
	}

	// Pass 3: agent.fallback_chain (drop entries that don't resolve
	// or that duplicate primary).
	if len(c.Agent.FallbackChain) > 0 {
		filtered := make([]string, 0, len(c.Agent.FallbackChain))
		for _, entry := range c.Agent.FallbackChain {
			if entry == c.Agent.Primary {
				logger.Warn("config sanitize: dropping fallback_chain entry — duplicates primary",
					zap.String("entry", entry))
				continue
			}
			if !c.endpointExists(entry) {
				logger.Warn("config sanitize: dropping fallback_chain entry — unresolved reference",
					zap.String("entry", entry))
				continue
			}
			filtered = append(filtered, entry)
		}
		c.Agent.FallbackChain = filtered
	}
}

// endpointExists reports whether a "provider:endpoint" dotted ref
// resolves to an existing (provider, endpoint) pair in cfg.LLM.
// Returns false on parse error.
func (c *Config) endpointExists(entry string) bool {
	prov, ep, err := ParseChainEntry(entry)
	if err != nil {
		return false
	}
	p, ok := c.LLM.Providers[prov]
	if !ok {
		return false
	}
	_, ok = p.Endpoints[ep]
	return ok
}

// Config is the top-level application configuration.
type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Log        LogConfig        `mapstructure:"log"`
	LLM        LLMConfig        `mapstructure:"llm"`
	Agent      AgentConfig      `mapstructure:"agent"`
	Engine     EngineConfig     `mapstructure:"engine"`
	Session    SessionConfig    `mapstructure:"session"`
	Channel    ChannelConfig    `mapstructure:"channels"`
	Tools      ToolsConfig      `mapstructure:"tools"`
	Permission PermissionConfig `mapstructure:"permission"`
	Skills     SkillsConfig     `mapstructure:"skills"`
	Agents     AgentsConfig     `mapstructure:"agents"`
	Console    ConsoleConfig    `mapstructure:"console"`

	// SourcePath is the absolute path of the yaml file viper
	// actually loaded — used by the management API to persist
	// mutations back to the same file the server started with.
	// Populated by Load at runtime; not a yaml field.
	SourcePath string `mapstructure:"-"`
}

// ThinkingIntensity enumerates the three coarse-grained reasoning
// effort levels we expose on the agent. Translated per provider by
// the adapter (OpenAI: reasoning_effort low/medium/high; Anthropic:
// thinking.budget_tokens in scaled token budgets; DeepSeek: enables
// the thinking mode regardless of level).
const (
	ThinkingIntensityLow    = "low"
	ThinkingIntensityMedium = "medium"
	ThinkingIntensityHigh   = "high"
)

// AgentConfig is the top-level routing + behavior config for the
// whole agent application. It describes:
//
//   - Which model to call (Primary + FallbackChain).
//   - Per-call defaults baked into each adapter (MaxTokens,
//     Temperature, ContextWindow).
//   - Conversation-level limits (MaxTurns LLM rounds, MaxToolCalls
//     tool-invocation rounds).
//   - Reasoning effort (ThinkingIntensity).
//
// The endpoint identifiers (Primary / FallbackChain entries) are
// dotted refs "provider:endpoint" pointing into llm.providers.
type AgentConfig struct {
	// Primary is the main model the agent calls. Single dotted ref
	// "provider:endpoint". Empty = no primary (degraded mode if
	// FallbackChain is also empty).
	Primary string `mapstructure:"primary"`

	// FallbackChain is the list of backup models tried in order
	// when Primary fails. Each entry is a dotted ref. May be empty.
	FallbackChain []string `mapstructure:"fallback_chain"`

	// MaxTokens is the agent-level default response cap. Applies
	// when an endpoint's own max_tokens is 0 OR when MaxTokens
	// here is smaller (endpoint acts as ceiling).
	MaxTokens int `mapstructure:"max_tokens"`

	// Temperature is the agent-level default sampling temperature
	// on a unified [0, 1] scale; the adapter scales it into the
	// target provider's native range (anthropic ×1, openai/gemini
	// ×2). 0 = fall back to the endpoint's own Temperature.
	Temperature float64 `mapstructure:"temperature"`

	// ContextWindow is the agent's working context budget in
	// tokens. Used by the engine for compaction thresholds and
	// shown to clients for UI. 0 = engine default.
	ContextWindow int `mapstructure:"context_window"`

	// MaxTurns caps the number of LLM rounds the engine spends on a
	// single user request before terminating with status="max_turns".
	// Must be ≥ 1 (Validate enforces). Moved here from EngineConfig:
	// it's an agent-behavior knob, not an engine plumbing knob.
	MaxTurns int `mapstructure:"max_turns"`

	// MaxToolCalls caps the cumulative number of tool invocations
	// across all turns of a single request. 0 = unlimited (the
	// MaxTurns ceiling still applies). Useful as a runaway-cost
	// safeguard when an agent gets stuck in a tool-call loop.
	MaxToolCalls int `mapstructure:"max_tool_calls"`

	// ThinkingIntensity is the reasoning-effort tier sent to
	// providers that support it. Must be one of low/medium/high
	// (case-sensitive) or empty (= don't downstream this hint, let
	// each endpoint's own EnableThinking decide). The per-provider
	// translation lives in the bifrost adapter.
	ThinkingIntensity string `mapstructure:"thinking_intensity"`
}

// ConsoleConfig holds Console management API server settings.
type ConsoleConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
}

// SkillsConfig holds skill loading settings.
type SkillsConfig struct {
	// Dirs is the list of directories to load skills from.
	// Each directory is scanned for SKILL.md (directory format) or *.md (flat format).
	// Earlier entries have higher priority on name conflict.
	Dirs []string `mapstructure:"dirs"`
}

// AgentsConfig holds project-level agent definition settings.
type AgentsConfig struct {
	// Dirs lists directories containing agent YAML files. Each directory is
	// scanned for *.yaml / *.yml files at server startup; definitions are
	// upserted to the store so changes take effect without recompilation.
	Dirs []string `mapstructure:"dirs"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level    string `mapstructure:"level"`  // debug, info, warn, error
	Format   string `mapstructure:"format"` // json, console
	Output   string `mapstructure:"output"` // stdout, file
	FilePath string `mapstructure:"file_path"`
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	Providers  map[string]ProviderConfig `mapstructure:"providers"`
	Bifrost    BifrostConfig             `mapstructure:"bifrost"`
	MaxRetries int                       `mapstructure:"max_retries"`
	APITimeout time.Duration             `mapstructure:"api_timeout"`
	// FirstByteTimeout caps how long the engine waits between Chat()
	// returning and the FIRST stream chunk landing. Disarms once the
	// first chunk arrives, so legitimate slow streams aren't penalised
	// — only catches the "gateway accepted the request but never sends
	// a byte" pathology that otherwise stays silent until the 10-min
	// orphan watchdog fires. Sized for upstream gateways that take
	// 10-20s for first byte under normal load.
	FirstByteTimeout time.Duration     `mapstructure:"first_byte_timeout"`
	ProxyURL         string            `mapstructure:"proxy_url"`
	CustomHeaders    map[string]string `mapstructure:"custom_headers"`

	// DefaultMaxTokens is the response cap inherited by endpoints
	// that omit max_tokens. Default 8192.
	DefaultMaxTokens int `mapstructure:"default_max_tokens"`

	// Health tunes per-provider cooldown behavior and the budgets
	// applied to the Failover dispatcher's three internal routing
	// tiers (Fast/Medium/Probe). Zero fields fall back to built-in
	// defaults (see ProviderHealthConfig docs).
	Health ProviderHealthConfig `mapstructure:"health"`
}

// ProviderHealthConfig tunes Failover dispatcher behavior: the
// exponential-backoff cooldown applied when a provider trips, plus
// the per-call wall-clock budgets that bound how long the dispatcher
// waits on a provider before advancing to the next chain entry.
//
// Defaults (applied when fields are zero):
//   - CooldownBase=30s, CooldownMax=10m, CooldownFactor=2
//   - PrimaryBudget=15s   (Fast — chain head with fallback behind it)
//   - LastHealthyBudget=30s (Medium — last Healthy / last-resort)
//   - ProbeBudget=5s      (Probe — eligibility check)
type ProviderHealthConfig struct {
	// CooldownBase is the cooldown applied on the FIRST trip after a
	// reset. Default 30s.
	CooldownBase time.Duration `mapstructure:"cooldown_base"`
	// CooldownMax caps the cooldown when consecutive trips compound.
	// Default 10m.
	CooldownMax time.Duration `mapstructure:"cooldown_max"`
	// CooldownFactor multiplies the cooldown on each consecutive trip
	// without intervening recovery. Default 2.
	CooldownFactor int `mapstructure:"cooldown_factor"`

	// PrimaryBudget caps the wall-clock duration spent on the
	// selected provider when at least one OTHER Healthy provider
	// remains as a fallback (FastPolicy). Default 15s.
	PrimaryBudget time.Duration `mapstructure:"primary_budget"`
	// LastHealthyBudget caps the wall-clock duration spent on the
	// selected provider when it is the LAST Healthy in the chain, or
	// when Tier 3 last-resort is invoked (MediumPolicy). Default 30s.
	LastHealthyBudget time.Duration `mapstructure:"last_healthy_budget"`
	// ProbeBudget caps the wall-clock duration spent on a probe call
	// (a tripped provider whose cooldown just expired; ProbePolicy).
	// Default 5s.
	ProbeBudget time.Duration `mapstructure:"probe_budget"`
}

// ProviderConfig holds a single vendor's credentials. One provider
// entry = one set of (type, base_url, api_key) reused by N endpoints
// underneath it. Changing a provider's api_key with PATCH rebuilds
// every endpoint's bifrost adapter atomically.
type ProviderConfig struct {
	// Type identifies the bifrost backend protocol. Required.
	// Allowed values are owned by the bifrost package (currently
	// openai and anthropic). Vendors with OpenAI-compatible APIs
	// (DeepSeek, Moonshot/Kimi, Zhipu/GLM, MiniMax, 讯飞, 通义)
	// use Type=openai plus a custom BaseURL.
	Type string `mapstructure:"type"`
	// BaseURL overrides the bifrost SDK's default endpoint. Required
	// for OpenAI-compatible vendors; optional otherwise.
	BaseURL string `mapstructure:"base_url"`
	// APIKey is the vendor's secret. Plaintext at rest; protect the
	// config file and the management API at the network layer.
	APIKey string `mapstructure:"api_key"`
	// Disabled, when true, parks the entire provider — every
	// endpoint under it is skipped by the failover dispatcher
	// regardless of its own Disabled flag. Effective disabled =
	// provider.Disabled || endpoint.Disabled. Default false.
	Disabled bool `mapstructure:"disabled"`
	// Endpoints is the map of chain-routable endpoints sharing this
	// provider's credentials. Each endpoint binds a single model.
	Endpoints map[string]EndpointConfig `mapstructure:"endpoints"`
}

// EndpointConfig holds the per-model knobs for a chain-routable
// endpoint nested under a provider. Inherits the parent provider's
// credentials (Type / BaseURL / APIKey) — the endpoint just picks
// the model and per-call tuning.
type EndpointConfig struct {
	// Model is the model id (interpreted by the parent provider's
	// type — e.g. "claude-sonnet-4-6" for type=anthropic,
	// "deepseek-v4-flash" for type=openai). Required.
	Model string `mapstructure:"model"`
	// MaxTokens caps the response length. 0 = inherit
	// LLMConfig.DefaultMaxTokens.
	MaxTokens int `mapstructure:"max_tokens"`
	// Temperature is the sampling temperature; 0 = provider default.
	Temperature float64 `mapstructure:"temperature"`
	// EnableThinking toggles vendor-side chain-of-thought output
	// (DeepSeek v3.1+ thinking models, OpenAI o-series, etc.).
	// nil = don't send (provider default); false = disable; true =
	// enable. The bifrost adapter translates this per-vendor.
	EnableThinking *bool `mapstructure:"enable_thinking"`
	// ContextWindow is the model's intrinsic context-window upper
	// bound in tokens (e.g. 200_000 for claude-4.x / gpt-5 / gemini
	// 2.x). Operator-configured per endpoint so the engine compactor
	// can cap agent.context_window against the actual model limit:
	// effective_context_window = min(agent.context_window,
	// endpoint.context_window), with 200_000 as the fallback when
	// both are unset. 0 = unset / use the framework fallback.
	ContextWindow int `mapstructure:"context_window"`
	// Disabled, when true, makes the failover dispatcher skip this
	// endpoint even if it appears in fallback_chain — useful for
	// temporarily parking a model without removing it from yaml.
	// Default false. yaml-omitted = enabled.
	Disabled bool `mapstructure:"disabled"`

	// ModelType is an optional capability declaration that overrides
	// the manifest's SupportsFlags for this endpoint. Each token maps
	// to one SupportsFlags field (see registry.SupportsFromTokens).
	// Unset / empty → fall back to the manifest baseline.
	//
	// Valid tokens: vision / pdf / audio / video / reasoning / tools / search.
	// Unknown tokens are dropped with a warn on startup; the PATCH
	// endpoint rejects them with 400.
	ModelType []string `mapstructure:"model_type" yaml:"model_type,omitempty"`
	// Group is a free-form display-only tag used by the renderer to
	// bucket endpoints under a heading (e.g. "GPT-5" groups every
	// gpt-5.x model in the Settings UI). Empty = ungrouped; renderer
	// falls back to its getModelGroup(id) heuristic.
	//
	// The engine ignores this field entirely — it does NOT influence
	// routing, failover health, chain reference syntax, or model
	// capability. Persisted via yaml so the value survives a restart
	// and is visible across multiple clients pointing at the same
	// engine.
	Group string `mapstructure:"group" yaml:"group,omitempty"`
}

// ParseChainEntry splits a fallback_chain string into (provider,
// endpoint). The canonical separator is ':' (provider:endpoint), so
// endpoint names may freely contain '.' (common in model identifiers
// like gpt-5.5 / claude-3.5-sonnet). For backward compatibility,
// inputs without ':' also try '.' as the separator — but emitting
// (via FormatChainEntry / yaml persistence / API responses) always
// uses ':' to drive operators toward the canonical form.
//
// Algorithm:
//
//  1. If the string contains ':', split on the FIRST ':'
//     (e.g. "openai:gpt-5.5" → "openai" + "gpt-5.5").
//  2. Otherwise, split on the FIRST '.' (legacy "openai.claude-46"
//     stays valid).
//  3. Empty segments / no separator → error.
//
// Returns an error when the string is empty, lacks any separator,
// or has an empty provider/endpoint segment.
func ParseChainEntry(s string) (provider, endpoint string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("chain entry is empty")
	}
	idx := strings.Index(s, ":")
	if idx < 0 {
		idx = strings.Index(s, ".")
	}
	if idx < 0 {
		return "", "", fmt.Errorf("chain entry %q missing ':' separator (expected provider:endpoint, e.g. \"openai:gpt-5.5\")", s)
	}
	provider = s[:idx]
	endpoint = s[idx+1:]
	if provider == "" || endpoint == "" {
		return "", "", fmt.Errorf("chain entry %q has empty provider or endpoint segment", s)
	}
	return provider, endpoint, nil
}

// FormatChainEntry produces the canonical "provider:endpoint" form.
// Always emits ':' regardless of the input form ParseChainEntry
// accepted.
func FormatChainEntry(provider, endpoint string) string {
	return provider + ":" + endpoint
}

// BifrostConfig holds Bifrost unified SDK settings.
// Bifrost is always enabled as the sole provider backend.
type BifrostConfig struct {
	Provider       string `mapstructure:"provider"`        // "anthropic", "openai", etc. Defaults to LLM.DefaultProvider.
	Model          string `mapstructure:"model"`           // Override model (defaults to provider's model).
	APIKey         string `mapstructure:"api_key"`         // Override API key (defaults to provider's key).
	BaseURL        string `mapstructure:"base_url"`        // Override base URL (defaults to provider's base_url).
	FallbackModel  string `mapstructure:"fallback_model"`  // Fallback model on primary failure.
	MaxConcurrency int    `mapstructure:"max_concurrency"` // 0 = Bifrost default (1000).
	BufferSize     int    `mapstructure:"buffer_size"`     // 0 = Bifrost default (5000).
}

// EngineConfig holds query engine settings.
//
// Note: max_turns moved to AgentConfig — it's an agent-behavior knob
// (caps LLM rounds per request) that the operator wants to manage
// alongside the routing config, not an engine plumbing knob.
type EngineConfig struct {
	AutoCompactThreshold float64       `mapstructure:"auto_compact_threshold"`
	ToolTimeout          time.Duration `mapstructure:"tool_timeout"`

	// MaxPlanReplans bounds how many times PlanCoordinator may re-plan
	// after Judge.ReviewGoal fails. Each re-plan is one Planner LLM call
	// plus a fresh execution of all steps it produces — set conservatively
	// to keep an unrecoverable goal from looping the planner indefinitely.
	// Reaching this cap no longer silently falls back: the coordinator
	// pauses and asks the user (via prompt.user) whether to keep trying.
	MaxPlanReplans int `mapstructure:"max_plan_replans"`

	// MaxStepAttempts bounds Scheduler retries on a transient step
	// failure (rate limit, network blip, provider 5xx). Two attempts
	// catches the bulk of flakes, three covers stickier transient
	// patterns. Reaching this cap pauses the plan and asks the user.
	MaxStepAttempts int `mapstructure:"max_step_attempts"`
}

// SessionConfig holds session management settings.
type SessionConfig struct {
	MaxMessages int           `mapstructure:"max_messages"`
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`
	DBPath      string        `mapstructure:"db_path"` // SQLite database file path
}

// ChannelConfig holds per-channel settings.
type ChannelConfig struct {
	Feishu    FeishuChannelConfig `mapstructure:"feishu"`
	WebSocket WSChannelConfig     `mapstructure:"websocket"`
	HTTP      HTTPChannelConfig   `mapstructure:"http"`
}

// FeishuChannelConfig holds Feishu bot settings.
type FeishuChannelConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Host      string `mapstructure:"host"`
	Port      int    `mapstructure:"port"`
	AppID     string `mapstructure:"app_id"`
	AppSecret string `mapstructure:"app_secret"`
}

// WSChannelConfig holds WebSocket settings.
type WSChannelConfig struct {
	Enabled        bool          `mapstructure:"enabled"`
	Host           string        `mapstructure:"host"`
	Port           int           `mapstructure:"port"`
	Path           string        `mapstructure:"path"`
	WriteBuffer    int           `mapstructure:"write_buffer"`     // per-connection write buffer size (default 256)
	PingInterval   time.Duration `mapstructure:"ping_interval"`    // keep-alive ping interval (default 30s)
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`    // single write deadline (default 10s)
	MaxMessageSize int64         `mapstructure:"max_message_size"` // max inbound frame size (default 512KB)
	ClientTools    bool          `mapstructure:"client_tools"`     // true = client executes tools; false = server executes tools
	// TraceFrames toggles per-outgoing-frame Debug log lines from
	// writePump: type / card_kind / card_id / parent / seq / status /
	// error. Default false (production). Useful when chasing
	// "client sees X step adds but only Y closes" lifecycle bugs —
	// flip to true, reproduce, grep "ws send" on the resulting log.
	TraceFrames bool `mapstructure:"trace_frames"`
}

// HTTPChannelConfig holds HTTP API settings.
type HTTPChannelConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
	Path    string `mapstructure:"path"`
}

// ToolsConfig holds per-tool settings.
type ToolsConfig struct {
	Bash         ToolConfig         `mapstructure:"bash"`
	FileRead     ToolConfig         `mapstructure:"file_read"`
	FileEdit     ToolConfig         `mapstructure:"file_edit"`
	FileWrite    ToolConfig         `mapstructure:"file_write"`
	Grep         ToolConfig         `mapstructure:"grep"`
	Glob         ToolConfig         `mapstructure:"glob"`
	WebFetch     ToolConfig         `mapstructure:"web_fetch"`
	WebSearch    WebSearchConfig    `mapstructure:"web_search"`
	TavilySearch TavilySearchConfig `mapstructure:"tavily_search"`
	BrowserAgent BrowserAgentConfig `mapstructure:"browser_agent"`
}

// ToolConfig holds individual tool settings.
type ToolConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Timeout     time.Duration `mapstructure:"timeout"`
	MaxFileSize string        `mapstructure:"max_file_size"`
	Sandbox     bool          `mapstructure:"sandbox"`
}

// WebSearchConfig holds settings for the iFlytek Spark v2/search tool.
// APIKey is the APIPassword issued by the iFly console (used verbatim
// in the `Authorization: Bearer <APIPassword>` header); the v2 endpoint
// is hardcoded in the tool implementation.
type WebSearchConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	APIKey  string `mapstructure:"api_key"`
	Limit   int    `mapstructure:"limit"`
}

// TavilySearchConfig holds settings for the Tavily Search API tool.
type TavilySearchConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	APIKey     string `mapstructure:"api_key"`
	MaxResults int    `mapstructure:"max_results"`
}

// BrowserAgentConfig holds settings for the browser-agent tool family.
type BrowserAgentConfig struct {
	Enabled              bool          `mapstructure:"enabled"`
	DefaultVisibility    string        `mapstructure:"default_visibility"`
	MaxSteps             int           `mapstructure:"max_steps"`
	BlockedDomains       []string      `mapstructure:"blocked_domains"`
	HumanTakeoverTimeout time.Duration `mapstructure:"human_takeover_timeout"`
	SessionPersistence   bool          `mapstructure:"session_persistence"`
	CLITimeout           time.Duration `mapstructure:"cli_timeout"`
	SkillMaxBytes        int           `mapstructure:"skill_max_bytes"`
	ContentBoundaries    bool          `mapstructure:"content_boundaries"`
	MaxOutputBytes       int           `mapstructure:"max_output_bytes"`
	AllowedDomains       []string      `mapstructure:"allowed_domains"`
	ActionPolicyPath     string        `mapstructure:"action_policy_path"`
	ConfirmActions       []string      `mapstructure:"confirm_actions"`
}

// PermissionConfig holds tool permission control settings.
type PermissionConfig struct {
	Mode         string   `mapstructure:"mode"` // default, plan, bypass, acceptEdits, dontAsk
	AllowedTools []string `mapstructure:"allowed_tools"`
	DeniedTools  []string `mapstructure:"denied_tools"`
}

// Load reads configuration from file, environment variables, and defaults.
func Load(configPath string) (*Config, error) {
	// KeyDelimiter "::" instead of the default "." so that yaml keys
	// containing dots (endpoint names like "gpt-5.5" /
	// "claude-3.5-sonnet") are NOT split into nested paths. With the
	// default ".", viper would interpret `providers.openai.endpoints.gpt-5.5`
	// as a 5-level nested path and merge "gpt-5.4" + "gpt-5.5" into a
	// single bogus "gpt-5" entry. Switching to "::" keeps yaml keys
	// opaque — internal SetDefault paths use "::" below for consistency.
	v := viper.NewWithOptions(viper.KeyDelimiter("::"))

	// Defaults
	v.SetDefault("server::host", "0.0.0.0")
	v.SetDefault("server::port", 8080)
	v.SetDefault("log::level", "info")
	v.SetDefault("log::format", "json")
	v.SetDefault("log::output", "stdout")
	v.SetDefault("llm::max_retries", 10)
	v.SetDefault("llm::api_timeout", "600s")
	v.SetDefault("llm::first_byte_timeout", "120s")
	v.SetDefault("llm::default_max_tokens", 8192)
	v.SetDefault("llm::health::cooldown_base", "30s")
	v.SetDefault("llm::health::cooldown_max", "10m")
	v.SetDefault("llm::health::cooldown_factor", 2)
	v.SetDefault("llm::health::primary_budget", "15s")
	v.SetDefault("llm::health::last_healthy_budget", "30s")
	v.SetDefault("llm::health::probe_budget", "5s")
	v.SetDefault("agent::max_turns", 50)
	v.SetDefault("agent::max_tool_calls", 0)
	v.SetDefault("engine::auto_compact_threshold", 0.8)
	v.SetDefault("engine::tool_timeout", "120s")
	v.SetDefault("engine::max_plan_replans", 3)
	v.SetDefault("engine::max_step_attempts", 3)
	v.SetDefault("session::max_messages", 200)
	v.SetDefault("session::idle_timeout", "30m")
	v.SetDefault("session::db_path", "~/.harnessclaw/db/sessions.db")
	v.SetDefault("channels::websocket::enabled", true)
	v.SetDefault("channels::websocket::host", "0.0.0.0")
	v.SetDefault("channels::websocket::port", 8081)
	v.SetDefault("channels::websocket::path", "/v1/ws")
	v.SetDefault("channels::websocket::write_buffer", 256)
	v.SetDefault("channels::websocket::ping_interval", "30s")
	v.SetDefault("channels::websocket::write_timeout", "10s")
	v.SetDefault("channels::websocket::max_message_size", 524288)
	v.SetDefault("channels::websocket::trace_frames", false)
	v.SetDefault("channels::http::enabled", true)
	v.SetDefault("channels::http::host", "0.0.0.0")
	v.SetDefault("channels::http::port", 8080)
	v.SetDefault("channels::http::path", "/api/v1")
	v.SetDefault("channels::feishu::enabled", false)
	v.SetDefault("channels::feishu::host", "0.0.0.0")
	v.SetDefault("channels::feishu::port", 8082)
	v.SetDefault("tools::bash::enabled", true)
	v.SetDefault("tools::bash::timeout", "60s")
	v.SetDefault("tools::file_read::enabled", true)
	v.SetDefault("tools::file_edit::enabled", true)
	v.SetDefault("tools::file_write::enabled", true)
	v.SetDefault("tools::grep::enabled", true)
	v.SetDefault("tools::glob::enabled", true)
	v.SetDefault("tools::web_fetch::enabled", true)
	v.SetDefault("tools::web_search::enabled", false)
	v.SetDefault("tools::web_search::limit", 5)
	v.SetDefault("tools::tavily_search::enabled", false)
	v.SetDefault("tools::tavily_search::max_results", 5)
	v.SetDefault("tools::browser_agent::enabled", false)
	v.SetDefault("tools::browser_agent::default_visibility", "hidden")
	v.SetDefault("tools::browser_agent::max_steps", 30)
	v.SetDefault("tools::browser_agent::blocked_domains", []string{})
	v.SetDefault("tools::browser_agent::human_takeover_timeout", "120s")
	v.SetDefault("tools::browser_agent::session_persistence", true)
	v.SetDefault("tools::browser_agent::cli_timeout", "25s")
	v.SetDefault("tools::browser_agent::skill_max_bytes", 200000)
	v.SetDefault("tools::browser_agent::content_boundaries", true)
	v.SetDefault("tools::browser_agent::max_output_bytes", 50000)
	v.SetDefault("tools::browser_agent::allowed_domains", []string{})
	v.SetDefault("tools::browser_agent::action_policy_path", "")
	v.SetDefault("tools::browser_agent::confirm_actions", []string{})
	v.SetDefault("permission::mode", "default")
	v.SetDefault("skills::dirs", []string{"~/.harnessclaw/workspace/skills"})
	v.SetDefault("console::enabled", true)
	v.SetDefault("console::host", "0.0.0.0")
	v.SetDefault("console::port", 8090)

	// Config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	// Environment variables: e.g. CLAUDE_SERVER_PORT -> server.port
	v.SetEnvPrefix("CLAUDE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// Config file not found is OK — use defaults + env vars
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Record the file viper actually loaded — providers management
	// API writes mutations back to this path. Empty when viper
	// found nothing on disk (all-default startup).
	if used := v.ConfigFileUsed(); used != "" {
		if abs, err := filepath.Abs(used); err == nil {
			cfg.SourcePath = abs
		} else {
			cfg.SourcePath = used
		}
	}

	// Apply default skills directory when config has no dirs.
	// Viper's SetDefault won't help if the key exists but the value is null/empty.
	if len(cfg.Skills.Dirs) == 0 {
		home, _ := os.UserHomeDir()
		cfg.Skills.Dirs = []string{filepath.Join(home, ".harnessclaw", "workspace", "skills")}
	}

	// Expand ~ in skill dirs to the user's home directory.
	expandSkillDirs(&cfg)

	// Expand ~ in agent dirs to the user's home directory.
	expandAgentDirs(&cfg)

	// Expand ~ in database paths to the user's home directory.
	expandHomePath(&cfg.Session.DBPath)

	return &cfg, nil
}

// expandHomePath replaces a ~ prefix with the user's home directory.
func expandHomePath(p *string) {
	if *p == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	if *p == "~" {
		*p = home
		return
	}
	if strings.HasPrefix(*p, "~/") || strings.HasPrefix(*p, "~\\") {
		*p = filepath.Join(home, filepath.FromSlash((*p)[2:]))
	}
}

// expandSkillDirs replaces ~ prefix with the user's home directory in skill paths,
// and normalizes path separators for the current platform.
func expandSkillDirs(cfg *Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for i, dir := range cfg.Skills.Dirs {
		if dir == "~" {
			cfg.Skills.Dirs[i] = home
			continue
		}
		// Match both ~/path and ~\path (Windows)
		if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~\\") {
			// Split the relative part after ~/ or ~\, then rejoin platform-aware
			rel := filepath.FromSlash(dir[2:])
			cfg.Skills.Dirs[i] = filepath.Join(home, rel)
			continue
		}
		// Normalize any forward slashes in explicit paths (e.g. from yaml on Windows)
		cfg.Skills.Dirs[i] = filepath.FromSlash(dir)
	}
}

// expandAgentDirs applies the same ~ expansion and path normalization to
// cfg.Agents.Dirs as expandSkillDirs does for skill directories.
func expandAgentDirs(cfg *Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for i, dir := range cfg.Agents.Dirs {
		if dir == "~" {
			cfg.Agents.Dirs[i] = home
			continue
		}
		if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~\\") {
			rel := filepath.FromSlash(dir[2:])
			cfg.Agents.Dirs[i] = filepath.Join(home, rel)
			continue
		}
		cfg.Agents.Dirs[i] = filepath.FromSlash(dir)
	}
}
