package manager

import "harnessclaw-go/internal/provider/failover"

// ProviderSnapshot is the API-facing view of a single provider
// (credentials) with its nested endpoints. APIKey is returned
// verbatim — protect /api/v1/providers at the network layer.
type ProviderSnapshot struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	// Disabled is true when the entire provider is parked — every
	// endpoint under it is skipped by the failover dispatcher
	// regardless of per-endpoint flags.
	Disabled  bool               `json:"disabled"`
	Endpoints []EndpointSnapshot `json:"endpoints"`
}

// EndpointSnapshot is the API-facing view of a single endpoint
// nested under a provider.
type EndpointSnapshot struct {
	Name           string  `json:"name"`
	Model          string  `json:"model"`
	MaxTokens      int     `json:"max_tokens"`
	Temperature    float64 `json:"temperature,omitempty"`
	EnableThinking *bool   `json:"enable_thinking,omitempty"`
	// ContextWindow is the model's intrinsic context-window upper
	// bound (operator-configured per endpoint, 0 = unset). The engine
	// caps the agent-level context_window against this value.
	ContextWindow int `json:"context_window,omitempty"`
	// Disabled is true when the endpoint is parked (skipped by the
	// failover dispatcher) regardless of chain membership.
	Disabled bool `json:"disabled"`
	// InChain reports whether the (provider, endpoint) pair appears
	// in the current fallback_chain. Endpoints can exist without
	// being routed (staged for later).
	InChain bool `json:"in_chain"`
	// ModelType is the endpoint's capability override token list.
	// Empty/nil means the endpoint inherits the manifest's
	// SupportsFlags baseline.
	ModelType []string `json:"model_type,omitempty"`
}

// AgentSnapshotPayload is the API-facing view of the agent-level
// routing config plus per-entry health for the effective chain
// (primary first, then fallback_chain in order). Entries[i] aligns
// with the effective chain index — Entries[0] is the primary's
// health when Primary is set.
type AgentSnapshotPayload struct {
	// Primary is the main routed endpoint as a dotted ref
	// "provider:endpoint". Empty means no primary configured.
	Primary string `json:"primary"`
	// FallbackChain lists backup endpoints tried in order after
	// Primary fails. Each entry is a dotted ref.
	FallbackChain []string `json:"fallback_chain"`
	// MaxTokens is the agent-level default response cap.
	MaxTokens int `json:"max_tokens"`
	// Temperature is the agent-level default sampling temperature
	// (unified [0, 1] scale, scaled per provider type at adapter
	// build time).
	Temperature float64 `json:"temperature,omitempty"`
	// ContextWindow is the agent's working context budget in tokens
	// as configured. The actual budget the engine compactor will use
	// is reported in EffectiveContextWindow (capped against the
	// primary endpoint's intrinsic ContextWindow).
	ContextWindow int `json:"context_window,omitempty"`
	// EffectiveContextWindow is min(agent.context_window,
	// primary_endpoint.context_window), with a 200_000 fallback when
	// both are unset. This is the value the engine compactor
	// actually uses for auto-compact thresholds. Surfaced here so
	// operators can see "agent.context_window was 500k but the
	// primary endpoint maxes out at 200k, so the effective budget
	// is 200k".
	EffectiveContextWindow int `json:"effective_context_window,omitempty"`
	// MaxTurns caps LLM rounds per user request.
	MaxTurns int `json:"max_turns,omitempty"`
	// MaxToolCalls caps total tool invocations across all turns of a
	// single request; 0 = unlimited.
	MaxToolCalls int `json:"max_tool_calls,omitempty"`
	// ThinkingIntensity is the reasoning-effort tier
	// (low/medium/high) sent to providers that support it; empty =
	// don't downstream the hint.
	ThinkingIntensity string `json:"thinking_intensity,omitempty"`
	// Entries is per-chain-entry health from the live dispatcher.
	// Index 0 corresponds to Primary; subsequent indices correspond
	// to FallbackChain entries. nil when no chain is configured.
	Entries []failover.ProviderHealth `json:"entries"`
}
