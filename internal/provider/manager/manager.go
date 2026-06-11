// Package manager provides a hot-swappable wrapper around the
// failover dispatcher. The Manager itself implements provider.Provider
// — engine code holds a stable reference to it and never sees the
// underlying Failover swap. Inside, the CRUD methods rebuild the
// chain and atomic-swap the live dispatcher so in-flight requests
// continue against the snapshot they were dispatched with, while
// subsequent requests use the new configuration.
//
// Data model (nested):
//
//	providers (map: credentials)
//	  └── endpoints (map: chain-routable model bindings)
//
//	fallback_chain ([]string of "provider.endpoint" dotted refs)
package manager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/provider/failover"
	modelregistry "harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/pkg/types"
)

// AdapterBuilder constructs one Bifrost adapter for a (provider,
// endpoint) pair. Manager passes the parent provider's credentials,
// the per-endpoint model+tuning, AND the live agent-level defaults
// (max_tokens / temperature) so the builder can pre-scale temperature
// into the target provider's legal range and pre-cap max_tokens
// against the endpoint's own MaxTokens, baking the result into the
// Adapter as its "default" applied when a ChatRequest leaves these
// fields zero.
type AdapterBuilder func(provName string, provCfg config.ProviderConfig, epName string, epCfg config.EndpointConfig, agent config.AgentConfig) (*bifrost.Adapter, error)

// PolicyBuilder constructs the three failover RetryPolicy values
// from the health config.
type PolicyBuilder func(h config.ProviderHealthConfig) (fast, medium, probe failover.RetryPolicy)

// Manager is the engine-facing provider.Provider plus a CRUD API
// for runtime configuration changes.
type Manager struct {
	logger         *zap.Logger
	registry       *modelregistry.Registry
	adapterBuilder AdapterBuilder
	policyBuilder  PolicyBuilder

	// current points to the live dispatcher. Reads via Load (Chat
	// path) are lock-free; writes (rebuild) hold mu.
	current atomic.Pointer[failover.Failover]

	// mu serialises configuration mutations. Read-paths (Chat,
	// snapshots) don't take it.
	mu sync.Mutex
	// cfg is the live LLM config snapshot — source of truth for
	// providers (credentials / endpoints).
	cfg config.LLMConfig
	// agent is the live agent-level routing config (primary +
	// fallback_chain + max_tokens / temperature / context_window).
	// Internal mutations still operate on a single "effective
	// chain" derived from Primary + FallbackChain; this struct is
	// the source of truth and the yaml-persisted shape.
	agent config.AgentConfig
	// adapters caches the live Bifrost adapter for each chain-eligible
	// endpoint, keyed by dotted "provider.endpoint". Reused across
	// rebuilds when nothing on the (provider, endpoint) tuple
	// changed, so a chain-only reorder doesn't dial new connections.
	adapters map[string]*bifrost.Adapter
}

// effectiveChain returns the single ordered list driving the
// Failover dispatcher: [Primary, ...FallbackChain] with duplicates
// removed and the primary always first. Caller holds m.mu.
func (m *Manager) effectiveChain() []string {
	out := make([]string, 0, 1+len(m.agent.FallbackChain))
	seen := map[string]bool{}
	if m.agent.Primary != "" {
		out = append(out, m.agent.Primary)
		seen[m.agent.Primary] = true
	}
	for _, e := range m.agent.FallbackChain {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// EffectiveContextWindow returns min(agent.ContextWindow, primary_endpoint.ContextWindow)
// with 200_000 fallback when neither is set. Primary endpoint is effectiveChain()[0]; if the
// chain is empty, only the agent value (or fallback) applies. Reported via
// AgentSnapshotPayload.EffectiveContextWindow so operators can see when their agent.context_window
// is being clamped by the endpoint's intrinsic limit.
func (m *Manager) EffectiveContextWindow() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effectiveContextWindowLocked()
}

// effectiveContextWindowLocked is the lock-free internal helper used from AgentSnapshot.
func (m *Manager) effectiveContextWindowLocked() int {
	primaryCW := 0
	chain := m.effectiveChain()
	if len(chain) > 0 {
		if prov, ep, err := config.ParseChainEntry(chain[0]); err == nil {
			if p, ok := m.cfg.Providers[prov]; ok {
				if e, ok := p.Endpoints[ep]; ok {
					primaryCW = e.ContextWindow
				}
			}
		}
	}
	return effectiveContextWindowCap(m.agent.ContextWindow, primaryCW)
}

// effectiveContextWindowCap is the pure function form of the cap policy.
// Standalone so the table test can exercise edges without building a Manager.
func effectiveContextWindowCap(agent, endpoint int) int {
	const fallback = 200_000
	switch {
	case agent <= 0 && endpoint <= 0:
		return fallback
	case endpoint <= 0:
		return agent
	case agent <= 0:
		return endpoint
	case agent > endpoint:
		return endpoint
	default:
		return agent
	}
}

// setEffectiveChain is the inverse: writes the given effective
// chain back into agent.Primary + agent.FallbackChain (chain[0] →
// Primary, chain[1:] → FallbackChain). Empty chain clears both.
// Caller holds m.mu.
func (m *Manager) setEffectiveChain(chain []string) {
	if len(chain) == 0 {
		m.agent.Primary = ""
		m.agent.FallbackChain = nil
		return
	}
	m.agent.Primary = chain[0]
	if len(chain) > 1 {
		m.agent.FallbackChain = append([]string(nil), chain[1:]...)
	} else {
		m.agent.FallbackChain = nil
	}
}

// New builds a Manager and warms the initial Failover dispatcher.
func New(
	cfg config.LLMConfig,
	agent config.AgentConfig,
	registry *modelregistry.Registry,
	adapterBuilder AdapterBuilder,
	policyBuilder PolicyBuilder,
	logger *zap.Logger,
) (*Manager, error) {
	if adapterBuilder == nil {
		return nil, errors.New("manager: AdapterBuilder is required")
	}
	if policyBuilder == nil {
		return nil, errors.New("manager: PolicyBuilder is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	m := &Manager{
		logger:         logger,
		registry:       registry,
		adapterBuilder: adapterBuilder,
		policyBuilder:  policyBuilder,
		cfg:            cloneLLMConfig(cfg),
		agent:          cloneAgentConfig(agent),
		adapters:       make(map[string]*bifrost.Adapter),
	}
	if err := m.rebuildLocked(); err != nil {
		return nil, fmt.Errorf("manager: initial build: %w", err)
	}
	return m, nil
}

// ---------- provider.Provider ----------

func (m *Manager) Name() string { return "managed" }

// ErrNoEndpoint is returned when the engine tries to LLM-call but
// the agent has no primary and an empty fallback chain. Operator
// should PATCH /agent with at least a primary to recover (or restart
// with a populated yaml).
var ErrNoEndpoint = errors.New("manager: no LLM endpoint configured (agent.primary and agent.fallback_chain both empty)")

func (m *Manager) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	fo := m.current.Load()
	if fo == nil {
		return nil, ErrNoEndpoint
	}
	return fo.Chat(ctx, req)
}

func (m *Manager) CountTokens(ctx context.Context, msgs []types.Message) (int, error) {
	fo := m.current.Load()
	if fo == nil {
		// Best-effort fallback: estimate from raw char count without
		// going through a provider (no provider exists). Matches
		// what bifrost adapters do today.
		total := 0
		for _, msg := range msgs {
			for _, b := range msg.Content {
				total += len(b.Text) + len(b.ToolInput) + len(b.ToolResult)
			}
		}
		return total / 4, nil
	}
	return fo.CountTokens(ctx, msgs)
}

// ---------- Snapshots ----------

// ProvidersSnapshot returns every provider entry along with the
// endpoints nested under it. API keys are returned in plaintext —
// gate access at the network layer. Sorted by provider name, with
// endpoints under each sorted by endpoint name, for stable client UX.
func (m *Manager) ProvidersSnapshot() []ProviderSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.effectiveChain()
	out := make([]ProviderSnapshot, 0, len(m.cfg.Providers))
	for name, p := range m.cfg.Providers {
		eps := make([]EndpointSnapshot, 0, len(p.Endpoints))
		for epName, ep := range p.Endpoints {
			var mt []string
			if len(ep.ModelType) > 0 {
				mt = append(mt, ep.ModelType...)
			}
			eps = append(eps, EndpointSnapshot{
				Name:               epName,
				Model:              ep.Model,
				MaxTokens:          ep.MaxTokens,
				Temperature:        ep.Temperature,
				EnableThinking:     ep.EnableThinking,
				ContextWindow:      ep.ContextWindow,
				Disabled:           ep.Disabled,
				InChain:            containsString(chain, config.FormatChainEntry(name, epName)),
				ModelType:          mt,
				Group:              ep.Group,
				ImageGenerationURL: m.imageGenerationURLLocked(name, p, ep),
			})
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].Name < eps[j].Name })
		out = append(out, ProviderSnapshot{
			Name:      name,
			Type:      p.Type,
			BaseURL:   p.BaseURL,
			APIKey:    p.APIKey,
			Disabled:  p.Disabled,
			Endpoints: eps,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) imageGenerationURLLocked(provName string, provCfg config.ProviderConfig, epCfg config.EndpointConfig) string {
	if !m.endpointSupportsImageGenerationLocked(provName, provCfg, epCfg) {
		return ""
	}
	provSpec := m.lookupProviderSpecLocked(provName, provCfg)
	if provSpec == nil || provSpec.Endpoints.ImagesGenerations == nil {
		return ""
	}
	endpointPath := strings.TrimSpace(*provSpec.Endpoints.ImagesGenerations)
	if endpointPath == "" {
		return ""
	}
	baseURL := strings.TrimSpace(provCfg.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(provSpec.BaseURL)
	}
	if baseURL == "" {
		return ""
	}
	return joinEndpointURL(baseURL, endpointPath)
}

func (m *Manager) lookupProviderSpecLocked(provName string, provCfg config.ProviderConfig) *modelregistry.ProviderSpec {
	if m.registry == nil {
		return nil
	}
	if spec := m.registry.LookupProvider(provName); spec != nil {
		return spec
	}
	return m.registry.LookupProvider(provCfg.Type)
}

func (m *Manager) endpointSupportsImageGenerationLocked(provName string, provCfg config.ProviderConfig, epCfg config.EndpointConfig) bool {
	if len(epCfg.ModelType) > 0 {
		return modelregistry.SupportsFromTokens(epCfg.ModelType).ImageGeneration
	}
	if m.registry == nil {
		return false
	}
	if spec := m.registry.LookupByProviderAndModelID(provName, epCfg.Model); spec != nil {
		return spec.Supports.ImageGeneration
	}
	if spec := m.registry.LookupByProviderAndModelID(provCfg.Type, epCfg.Model); spec != nil {
		return spec.Supports.ImageGeneration
	}
	return false
}

func joinEndpointURL(baseURL, endpointPath string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(endpointPath, "/")
	}
	u.Path = path.Join(u.Path, endpointPath)
	return u.String()
}

// AgentSnapshot returns the current agent routing config (primary +
// fallback_chain + shared per-call defaults + behavior limits) plus
// per-entry health for the effective chain. Returns empty primary +
// nil entries when running in degraded mode (no primary or chain
// configured).
func (m *Manager) AgentSnapshot() AgentSnapshotPayload {
	m.mu.Lock()
	payload := AgentSnapshotPayload{
		Primary:                m.agent.Primary,
		FallbackChain:          append([]string(nil), m.agent.FallbackChain...),
		ImageGeneration:        m.agent.ImageGeneration,
		MaxTokens:              m.agent.MaxTokens,
		Temperature:            m.agent.Temperature,
		ContextWindow:          m.agent.ContextWindow,
		EffectiveContextWindow: m.effectiveContextWindowLocked(),
		MaxTurns:               m.agent.MaxTurns,
		MaxToolCalls:           m.agent.MaxToolCalls,
		ThinkingIntensity:      m.agent.ThinkingIntensity,
	}
	m.mu.Unlock()
	fo := m.current.Load()
	if fo != nil {
		payload.Entries = fo.Snapshot()
	}
	return payload
}

// CurrentAgent returns a deep copy of the live agent config (used by
// the persistence layer to mirror state to yaml).
func (m *Manager) CurrentAgent() config.AgentConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneAgentConfig(m.agent)
}

// ActiveModelKey returns the engine-active provider:endpoint reference,
// matching the manifest key shape (e.g. "anthropic:claude-opus-4-7").
// Returns "" when no primary is configured. Mirrors CurrentAgent's
// locking discipline.
func (m *Manager) ActiveModelKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.agent.Primary
}

// ChainSupports returns the AND-intersection of SupportsFlags across
// the primary + every fallback entry. Used by the multimodal gate to
// reject inputs that would fail mid-chain on fail-over.
//
// Conservative semantics: if ANY chain member can't handle a
// modality, the gate rejects the user message upfront — even when the
// primary alone could process it. The trade-off is correctness over
// availability: a user dropping an image and seeing "switch model"
// is better than the request succeeding on primary, throwing on
// fallback hop, and surfacing as an opaque 400 from the upstream
// provider.
//
// `lookup` is a SupportsFlags resolver; callers wire it to a registry
// lookup. Returning a zero SupportsFlags for an unknown key (which
// is what LookupModel does for missing entries) intersects to
// all-false — that's the safe default for unmapped models.
//
// Returns a zero SupportsFlags when no primary is configured.
func (m *Manager) ChainSupports(lookup func(modelKey string) modelregistry.SupportsFlags) modelregistry.SupportsFlags {
	m.mu.Lock()
	chain := make([]string, 0, 1+len(m.agent.FallbackChain))
	if m.agent.Primary != "" {
		chain = append(chain, m.agent.Primary)
	}
	chain = append(chain, m.agent.FallbackChain...)
	m.mu.Unlock()

	if len(chain) == 0 {
		return modelregistry.SupportsFlags{}
	}

	first := true
	var acc modelregistry.SupportsFlags
	for _, key := range chain {
		if key == "" {
			continue
		}
		s := lookup(key)
		if first {
			acc = s
			first = false
			continue
		}
		acc = intersectSupports(acc, s)
	}
	if first {
		// Chain had only empty entries — fail-closed.
		return modelregistry.SupportsFlags{}
	}
	return acc
}

// LookupEndpointModelType returns the configured model_type tokens
// for a chain-ref key ("provider:endpoint"). Returns (nil, false)
// when the endpoint isn't configured OR when ModelType is empty —
// both cases mean "no override; fall back to manifest baseline".
//
// Unknown tokens have already been filtered at config-load time
// (see cmd/server.warnAndDropUnknownTokens); this method returns
// the canonical, validated list.
func (m *Manager) LookupEndpointModelType(key string) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prov, ep, err := config.ParseChainEntry(key)
	if err != nil {
		return nil, false
	}
	pc, ok := m.cfg.Providers[prov]
	if !ok {
		return nil, false
	}
	ec, ok := pc.Endpoints[ep]
	if !ok {
		return nil, false
	}
	if len(ec.ModelType) == 0 {
		return nil, false
	}
	// Defensive copy so callers can't mutate manager state.
	out := make([]string, len(ec.ModelType))
	copy(out, ec.ModelType)
	return out, true
}

// intersectSupports applies field-wise AND to every bool flag in
// SupportsFlags. Slice / structured fields are not intersected; the
// router's gate doesn't consult them.
func intersectSupports(a, b modelregistry.SupportsFlags) modelregistry.SupportsFlags {
	return modelregistry.SupportsFlags{
		Vision:                  a.Vision && b.Vision,
		PDFInput:                a.PDFInput && b.PDFInput,
		AudioInput:              a.AudioInput && b.AudioInput,
		VideoInput:              a.VideoInput && b.VideoInput,
		AudioOutput:             a.AudioOutput && b.AudioOutput,
		ImageGeneration:         a.ImageGeneration && b.ImageGeneration,
		Streaming:               a.Streaming && b.Streaming,
		SystemMessages:          a.SystemMessages && b.SystemMessages,
		StructuredOutput:        a.StructuredOutput && b.StructuredOutput,
		FunctionCalling:         a.FunctionCalling && b.FunctionCalling,
		ParallelFunctionCalling: a.ParallelFunctionCalling && b.ParallelFunctionCalling,
		ToolChoice:              a.ToolChoice && b.ToolChoice,
		ComputerUse:             a.ComputerUse && b.ComputerUse,
		WebSearch:               a.WebSearch && b.WebSearch,
		Reasoning:               a.Reasoning && b.Reasoning,
		ReasoningCanDisable:     a.ReasoningCanDisable && b.ReasoningCanDisable,
		PromptCaching:           a.PromptCaching && b.PromptCaching,
		ExplicitCacheControl:    a.ExplicitCacheControl && b.ExplicitCacheControl,
	}
}

// CurrentConfig returns a deep copy of the live LLM config (used by
// the persistence layer to mirror state to yaml).
func (m *Manager) CurrentConfig() config.LLMConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneLLMConfig(m.cfg)
}

// ---------- Mutations: provider credentials ----------

// ProviderCredsPatch is the partial update accepted by
// UpdateProviderCreds. nil fields mean "leave unchanged".
type ProviderCredsPatch struct {
	Type     *string
	BaseURL  *string
	APIKey   *string
	Disabled *bool
}

// IsEmpty reports whether the patch would change anything.
func (p ProviderCredsPatch) IsEmpty() bool {
	return p.Type == nil && p.BaseURL == nil && p.APIKey == nil && p.Disabled == nil
}

// UpdateProviderCreds applies a partial patch to llm.providers[name]
// credentials. Every endpoint under this provider gets its Bifrost
// adapter rebuilt to use the new credentials, then a new Failover
// dispatcher is atomically swapped in.
//
// Errors out (in-memory state unchanged) when:
//   - patch is empty
//   - provider doesn't exist
//   - new type is not in the bifrost-allowed list
//   - any endpoint's new adapter fails to build
func (m *Manager) UpdateProviderCreds(name string, patch ProviderCredsPatch) error {
	if patch.IsEmpty() {
		return errors.New("manager: empty provider creds patch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	provCfg, ok := m.cfg.Providers[name]
	if !ok {
		return fmt.Errorf("manager: unknown provider %q", name)
	}
	if patch.Type != nil {
		if _, ok := bifrost.ProviderTypeOf(*patch.Type); !ok {
			return fmt.Errorf("manager: type %q not allowed", *patch.Type)
		}
		provCfg.Type = *patch.Type
	}
	if patch.BaseURL != nil {
		provCfg.BaseURL = *patch.BaseURL
	}
	if patch.APIKey != nil {
		provCfg.APIKey = *patch.APIKey
	}
	if patch.Disabled != nil {
		provCfg.Disabled = *patch.Disabled
	}

	// Build replacement adapters BEFORE mutating the cache so a
	// per-endpoint failure leaves state untouched.
	newAdapters := make(map[string]*bifrost.Adapter, len(provCfg.Endpoints))
	for epName, epCfg := range provCfg.Endpoints {
		a, err := m.adapterBuilder(name, provCfg, epName, epCfg, m.agent)
		if err != nil {
			return fmt.Errorf("manager: build adapter for %s.%s: %w", name, epName, err)
		}
		newAdapters[adapterKey(name, epName)] = a
	}

	// Commit.
	old := m.cfg.Providers[name]
	m.cfg.Providers[name] = provCfg
	// Track adapters being replaced so we can Shutdown them after
	// the dispatcher swap.
	var toShutdown []*bifrost.Adapter
	for k, a := range newAdapters {
		if prev, ok := m.adapters[k]; ok && prev != a {
			toShutdown = append(toShutdown, prev)
		}
		m.adapters[k] = a
	}
	// Disabling the provider auto-removes every chain entry under
	// it — same intent as endpoint-level disable, just scoped to
	// all endpoints. Re-enabling does NOT auto-restore.
	chainRemoved := 0
	if patch.Disabled != nil && *patch.Disabled {
		filtered, removed := filterChainProvider(m.effectiveChain(), name)
		if removed > 0 {
			m.setEffectiveChain(filtered)
			chainRemoved = removed
			m.logger.Info("manager: auto-removed disabled provider's chain entries",
				zap.String("provider", name),
				zap.Int("removed_count", removed))
		}
	}
	// Re-enabling the provider (or any other patch leaving it
	// enabled) + chain empty → promote the alphabetically-first
	// enabled endpoint under this provider to chain[0] so server
	// leaves degraded mode automatically.
	chainAutoFilled := false
	if !provCfg.Disabled && len(m.effectiveChain()) == 0 {
		if first := m.firstEnabledEndpointName(name); first != "" {
			chainAutoFilled = m.autoFillEmptyChain(name, first)
		}
	}
	if err := m.rebuildLocked(); err != nil {
		// Rollback adapter cache + config.
		m.cfg.Providers[name] = old
		// Best-effort: leave new adapters in cache (rebuildLocked
		// reads them); next rebuild will use the rolled-back config
		// and pick up the OLD endpoints' adapters from existing
		// cache entries (which weren't replaced).
		return fmt.Errorf("manager: rebuild after creds patch: %w", err)
	}
	for _, a := range toShutdown {
		a.Shutdown()
	}
	m.logger.Info("manager: provider creds updated",
		zap.String("provider", name),
		zap.Bool("type_changed", patch.Type != nil),
		zap.Bool("base_url_changed", patch.BaseURL != nil),
		zap.Bool("api_key_changed", patch.APIKey != nil),
		zap.Bool("disabled_changed", patch.Disabled != nil),
		zap.Int("endpoints_rebuilt", len(newAdapters)),
		zap.Int("chain_entries_removed", chainRemoved),
		zap.Bool("chain_auto_filled", chainAutoFilled),
	)
	return nil
}

// ---------- Mutations: provider creation ----------

// AddProvider registers a new provider entry under llm.providers.
// The new provider starts with no endpoints; callers add endpoints
// via AddEndpoint afterwards, then PUT the chain to include them.
//
// Errors out (in-memory state unchanged) when:
//   - name is empty, or contains ':' / '.'
//   - cfg.Type is empty / not in bifrost-allowed list
//   - a provider with this name already exists
func (m *Manager) AddProvider(name string, cfg config.ProviderConfig) error {
	if name == "" {
		return errors.New("manager: provider name required")
	}
	if strings.ContainsAny(name, ":.") {
		return fmt.Errorf("manager: provider name %q cannot contain ':' or '.'", name)
	}
	if cfg.Type == "" {
		return errors.New("manager: provider.type is required")
	}
	if _, ok := bifrost.ProviderTypeOf(cfg.Type); !ok {
		return fmt.Errorf("manager: type %q not allowed", cfg.Type)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.cfg.Providers[name]; exists {
		return fmt.Errorf("manager: provider %q already exists", name)
	}
	// Start fresh — caller will POST endpoints to populate.
	if cfg.Endpoints == nil {
		cfg.Endpoints = map[string]config.EndpointConfig{}
	}
	m.cfg.Providers[name] = cfg

	// Rebuild is a no-op for routing (chain doesn't reference this
	// new provider yet) but we run it to keep the dispatcher's
	// internal state consistent with cfg.
	if err := m.rebuildLocked(); err != nil {
		delete(m.cfg.Providers, name)
		return fmt.Errorf("manager: rebuild after add provider: %w", err)
	}
	m.logger.Info("manager: provider added",
		zap.String("name", name),
		zap.String("type", cfg.Type),
		zap.String("base_url", cfg.BaseURL),
	)
	return nil
}

// ---------- Mutations: endpoint CRUD ----------

// EndpointPatch is the partial update accepted by UpdateEndpoint.
// nil fields mean "leave unchanged". ModelType is a *[]string so
// callers can distinguish "omitted" (nil) from "explicitly cleared"
// (non-nil pointer to empty slice — clears the override and reverts
// the endpoint to manifest baseline).
type EndpointPatch struct {
	Model          *string
	MaxTokens      *int
	Temperature    *float64
	EnableThinking *bool
	Disabled       *bool
	ModelType      *[]string
	// Group sets the display-only tag. nil = leave alone; non-nil
	// pointer to "" = explicitly clear (yaml `group:` key removed on
	// persist); non-nil to a string = set/replace.
	Group *string
}

// IsEmpty reports whether the patch would change anything.
func (p EndpointPatch) IsEmpty() bool {
	return p.Model == nil && p.MaxTokens == nil && p.Temperature == nil &&
		p.EnableThinking == nil && p.Disabled == nil && p.ModelType == nil &&
		p.Group == nil
}

// AddEndpoint inserts a new endpoint under the named provider.
// `model` is required; other fields fall back to LLMConfig defaults
// (MaxTokens = LLMConfig.DefaultMaxTokens, Temperature/EnableThinking
// = provider/SDK default). The new endpoint is NOT automatically
// added to fallback_chain — call ReplaceChain separately.
func (m *Manager) AddEndpoint(provName, epName string, ep config.EndpointConfig) error {
	if provName == "" || epName == "" {
		return errors.New("manager: provider and endpoint name required")
	}
	if ep.Model == "" {
		return errors.New("manager: endpoint.model is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	provCfg, ok := m.cfg.Providers[provName]
	if !ok {
		return fmt.Errorf("manager: unknown provider %q", provName)
	}
	if _, exists := provCfg.Endpoints[epName]; exists {
		return fmt.Errorf("manager: endpoint %s.%s already exists", provName, epName)
	}
	if ep.MaxTokens == 0 {
		ep.MaxTokens = m.defaultMaxTokens()
	}
	// Build adapter eagerly so a bad config errors out before commit.
	adapter, err := m.adapterBuilder(provName, provCfg, epName, ep, m.agent)
	if err != nil {
		return fmt.Errorf("manager: build adapter for %s.%s: %w", provName, epName, err)
	}
	if provCfg.Endpoints == nil {
		provCfg.Endpoints = map[string]config.EndpointConfig{}
	}
	provCfg.Endpoints[epName] = ep
	m.cfg.Providers[provName] = provCfg
	m.adapters[adapterKey(provName, epName)] = adapter
	// When chain is empty AND the new endpoint is enabled, auto-promote
	// it to chain[0]. Otherwise stays staged until a manual PUT chain.
	chainAutoFilled := m.autoFillEmptyChain(provName, epName)
	if err := m.rebuildLocked(); err != nil {
		return fmt.Errorf("manager: rebuild after add endpoint: %w", err)
	}
	m.logger.Info("manager: endpoint added",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
		zap.String("model", ep.Model),
		zap.Bool("chain_auto_filled", chainAutoFilled),
	)
	return nil
}

// UpdateEndpoint applies a partial patch to an existing endpoint and
// rebuilds its adapter.
func (m *Manager) UpdateEndpoint(provName, epName string, patch EndpointPatch) error {
	if patch.IsEmpty() {
		return errors.New("manager: empty endpoint patch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	provCfg, ok := m.cfg.Providers[provName]
	if !ok {
		return fmt.Errorf("manager: unknown provider %q", provName)
	}
	ep, ok := provCfg.Endpoints[epName]
	if !ok {
		return fmt.Errorf("manager: endpoint %s.%s not found", provName, epName)
	}
	if patch.Model != nil {
		ep.Model = *patch.Model
	}
	if patch.MaxTokens != nil {
		ep.MaxTokens = *patch.MaxTokens
	}
	if patch.Temperature != nil {
		ep.Temperature = *patch.Temperature
	}
	if patch.EnableThinking != nil {
		v := *patch.EnableThinking
		ep.EnableThinking = &v
	}
	if patch.Disabled != nil {
		ep.Disabled = *patch.Disabled
	}
	if patch.ModelType != nil {
		if len(*patch.ModelType) == 0 {
			ep.ModelType = nil
		} else {
			ep.ModelType = append([]string(nil), (*patch.ModelType)...)
		}
	}
	if patch.Group != nil {
		ep.Group = *patch.Group
	}

	newAdapter, err := m.adapterBuilder(provName, provCfg, epName, ep, m.agent)
	if err != nil {
		return fmt.Errorf("manager: build adapter for %s.%s: %w", provName, epName, err)
	}
	oldAdapter := m.adapters[adapterKey(provName, epName)]
	provCfg.Endpoints[epName] = ep
	m.cfg.Providers[provName] = provCfg
	m.adapters[adapterKey(provName, epName)] = newAdapter
	// Disabling an endpoint auto-removes its chain entry — keeps the
	// chain in sync with operator intent (don't route through it).
	// Re-enabling does NOT auto-add it back: chain composition is
	// explicit, an enabled endpoint that's not in chain stays
	// staged until PUT /fallback-chain.
	chainChanged := false
	if patch.Disabled != nil && *patch.Disabled {
		dotted := config.FormatChainEntry(provName, epName)
		if filtered, removed := filterChainEntry(m.effectiveChain(), provName, epName); removed {
			m.setEffectiveChain(filtered)
			chainChanged = true
			m.logger.Info("manager: auto-removed disabled endpoint from chain",
				zap.String("entry", dotted))
		}
	}
	// If this patch turned the endpoint INTO an enabled state AND
	// chain is now empty, auto-promote it to chain[0] so the server
	// leaves degraded mode immediately. effectiveEnabled also checks
	// the parent provider's disabled flag.
	if m.autoFillEmptyChain(provName, epName) {
		chainChanged = true
	}
	if err := m.rebuildLocked(); err != nil {
		return fmt.Errorf("manager: rebuild after endpoint patch: %w", err)
	}
	if oldAdapter != nil && oldAdapter != newAdapter {
		oldAdapter.Shutdown()
	}
	m.logger.Info("manager: endpoint updated",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
		zap.Bool("chain_changed", chainChanged),
	)
	return nil
}

// DeleteEndpoint removes an endpoint. If it appears in the current
// fallback_chain, it is auto-removed from the chain (per user spec).
// The endpoint's bifrost adapter is shut down after the dispatcher
// swap.
func (m *Manager) DeleteEndpoint(provName, epName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	provCfg, ok := m.cfg.Providers[provName]
	if !ok {
		return fmt.Errorf("manager: unknown provider %q", provName)
	}
	if _, ok := provCfg.Endpoints[epName]; !ok {
		return fmt.Errorf("manager: endpoint %s.%s not found", provName, epName)
	}
	dotted := config.FormatChainEntry(provName, epName)
	removedFromChain := false
	currentChain := m.effectiveChain()
	if containsString(currentChain, dotted) {
		filtered := make([]string, 0, len(currentChain))
		for _, c := range currentChain {
			if c != dotted {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("manager: refusing to delete %s — it is the last chain entry; add another endpoint to the chain first", dotted)
		}
		m.setEffectiveChain(filtered)
		removedFromChain = true
	}
	delete(provCfg.Endpoints, epName)
	m.cfg.Providers[provName] = provCfg
	key := adapterKey(provName, epName)
	oldAdapter := m.adapters[key]
	delete(m.adapters, key)
	if err := m.rebuildLocked(); err != nil {
		return fmt.Errorf("manager: rebuild after delete: %w", err)
	}
	if oldAdapter != nil {
		oldAdapter.Shutdown()
	}
	m.logger.Info("manager: endpoint deleted",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
		zap.Bool("removed_from_chain", removedFromChain),
	)
	return nil
}

// ---------- Mutations: agent ----------

// AgentPatch is the partial update accepted by UpdateAgent. nil
// fields mean "leave unchanged". FallbackChain is a *[]string so
// callers can distinguish "leave alone" (nil) from "set to empty
// list" (non-nil pointer to zero-length slice).
type AgentPatch struct {
	Primary           *string
	FallbackChain     *[]string
	ImageGeneration   *string
	VideoGeneration   *string
	MaxTokens         *int
	Temperature       *float64
	ContextWindow     *int
	MaxTurns          *int
	MaxToolCalls      *int
	ThinkingIntensity *string
}

// IsEmpty reports whether the patch would change anything.
func (p AgentPatch) IsEmpty() bool {
	return p.Primary == nil && p.FallbackChain == nil && p.ImageGeneration == nil &&
		p.VideoGeneration == nil &&
		p.MaxTokens == nil && p.Temperature == nil && p.ContextWindow == nil &&
		p.MaxTurns == nil && p.MaxToolCalls == nil && p.ThinkingIntensity == nil
}

// UpdateAgent applies a partial patch to the agent config (primary,
// fallback_chain, max_tokens, temperature, context_window). Each
// chain ref (primary + every fallback_chain entry) must resolve to
// an existing (provider, endpoint). FallbackChain entries that
// duplicate Primary are rejected.
//
// Setting Primary="" with FallbackChain=[] (or both empty after
// patch) is permitted and lands the dispatcher in degraded mode.
//
// Errors out (in-memory state unchanged) when:
//   - patch is empty
//   - any reference doesn't resolve
//   - a fallback_chain entry duplicates the (new) primary
//   - rebuild fails (rolled back)
func (m *Manager) UpdateAgent(patch AgentPatch) error {
	if patch.IsEmpty() {
		return errors.New("manager: empty agent patch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	next := cloneAgentConfig(m.agent)
	if patch.Primary != nil {
		next.Primary = *patch.Primary
	}
	if patch.FallbackChain != nil {
		next.FallbackChain = append([]string(nil), (*patch.FallbackChain)...)
	}
	if patch.ImageGeneration != nil {
		next.ImageGeneration = *patch.ImageGeneration
	}
	if patch.VideoGeneration != nil {
		next.VideoGeneration = *patch.VideoGeneration
	}
	if patch.MaxTokens != nil {
		next.MaxTokens = *patch.MaxTokens
	}
	if patch.Temperature != nil {
		next.Temperature = *patch.Temperature
	}
	if patch.ContextWindow != nil {
		next.ContextWindow = *patch.ContextWindow
	}
	if patch.MaxTurns != nil {
		next.MaxTurns = *patch.MaxTurns
	}
	if patch.MaxToolCalls != nil {
		next.MaxToolCalls = *patch.MaxToolCalls
	}
	if patch.ThinkingIntensity != nil {
		next.ThinkingIntensity = *patch.ThinkingIntensity
	}

	if next.Primary != "" {
		if err := m.validateChainEntryLocked(next.Primary); err != nil {
			return err
		}
	}
	for _, entry := range next.FallbackChain {
		if err := m.validateChainEntryLocked(entry); err != nil {
			return err
		}
		if entry == next.Primary {
			return fmt.Errorf("manager: fallback_chain entry %q duplicates primary", entry)
		}
	}
	if next.ImageGeneration != "" {
		if err := m.validateChainEntryLocked(next.ImageGeneration); err != nil {
			return err
		}
	}
	// Validate ONLY fields the caller actually sent — operators can
	// run with MaxTurns=0 (engine falls back to its own default) in
	// tests / lightweight setups, so we don't enforce ≥1 here unless
	// the patch tries to set a zero/negative.
	if patch.MaxTurns != nil && *patch.MaxTurns < 1 {
		return fmt.Errorf("manager: agent.max_turns must be ≥ 1, got %d", *patch.MaxTurns)
	}
	if patch.MaxToolCalls != nil && *patch.MaxToolCalls < 0 {
		return fmt.Errorf("manager: agent.max_tool_calls must be ≥ 0 (0 = unlimited), got %d", *patch.MaxToolCalls)
	}
	if patch.ThinkingIntensity != nil && *patch.ThinkingIntensity != "" {
		switch *patch.ThinkingIntensity {
		case config.ThinkingIntensityLow, config.ThinkingIntensityMedium, config.ThinkingIntensityHigh:
		default:
			return fmt.Errorf("manager: agent.thinking_intensity must be low/medium/high (or empty), got %q", *patch.ThinkingIntensity)
		}
	}

	prev := m.agent
	m.agent = next
	// MaxTokens / Temperature changes affect the defaults baked into
	// each adapter. Invalidate the adapter cache so rebuildLocked
	// re-runs adapterBuilder with the new agent values — necessary
	// because Adapter is constructed with the resolved defaults frozen
	// at build time, not read per-call.
	tuningChanged := (patch.MaxTokens != nil && *patch.MaxTokens != prev.MaxTokens) ||
		(patch.Temperature != nil && *patch.Temperature != prev.Temperature)
	var oldAdapters map[string]*bifrost.Adapter
	if tuningChanged {
		oldAdapters = m.adapters
		m.adapters = make(map[string]*bifrost.Adapter, len(oldAdapters))
	}
	if err := m.rebuildLocked(); err != nil {
		m.agent = prev
		if tuningChanged {
			m.adapters = oldAdapters
		}
		return fmt.Errorf("manager: rebuild after agent patch: %w", err)
	}
	// Successful rebuild — replace cache means the old adapters are
	// orphaned. Shutdown them after the swap so in-flight requests
	// finish on the previous instances.
	for _, a := range oldAdapters {
		if a != nil {
			a.Shutdown()
		}
	}
	m.logger.Info("manager: agent config updated",
		zap.Bool("primary_changed", patch.Primary != nil),
		zap.Bool("fallback_chain_changed", patch.FallbackChain != nil),
		zap.Bool("image_generation_changed", patch.ImageGeneration != nil),
		zap.Bool("video_generation_changed", patch.VideoGeneration != nil),
		zap.Bool("max_tokens_changed", patch.MaxTokens != nil),
		zap.Bool("temperature_changed", patch.Temperature != nil),
		zap.Bool("context_window_changed", patch.ContextWindow != nil),
		zap.Bool("max_turns_changed", patch.MaxTurns != nil),
		zap.Bool("max_tool_calls_changed", patch.MaxToolCalls != nil),
		zap.Bool("thinking_intensity_changed", patch.ThinkingIntensity != nil),
		zap.String("primary", next.Primary),
		zap.Strings("fallback_chain", next.FallbackChain),
		zap.String("image_generation", next.ImageGeneration),
		zap.String("video_generation", next.VideoGeneration),
		zap.String("thinking_intensity", next.ThinkingIntensity),
	)
	return nil
}

// validateChainEntryLocked checks that entry parses and resolves to
// an existing (provider, endpoint) in cfg. Caller holds m.mu.
func (m *Manager) validateChainEntryLocked(entry string) error {
	prov, ep, err := config.ParseChainEntry(entry)
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	p, ok := m.cfg.Providers[prov]
	if !ok {
		return fmt.Errorf("manager: chain entry %q: provider %q does not exist", entry, prov)
	}
	if _, ok := p.Endpoints[ep]; !ok {
		return fmt.Errorf("manager: chain entry %q: endpoint %q does not exist under %q", entry, ep, prov)
	}
	return nil
}

// ReplaceChain is a back-compat wrapper around UpdateAgent that sets
// only the effective chain (chain[0] → primary, chain[1:] →
// fallback_chain). Retained for internal callers and existing tests;
// new code should call UpdateAgent directly.
func (m *Manager) ReplaceChain(newChain []string) error {
	if len(newChain) == 0 {
		return errors.New("manager: fallback_chain cannot be empty")
	}
	primary := newChain[0]
	rest := append([]string(nil), newChain[1:]...)
	return m.UpdateAgent(AgentPatch{
		Primary:       &primary,
		FallbackChain: &rest,
	})
}

// ---------- internal ----------

// rebuildLocked constructs a new Failover dispatcher from the
// current cfg + adapter cache and atomic-swaps it. Caller holds mu.
//
// Empty chain is a valid degraded state — Manager is still
// instantiated so the management API can mount, but current is
// cleared so Chat() returns a clear "no endpoint configured" error
// until the operator populates the chain via PUT /fallback-chain.
func (m *Manager) rebuildLocked() error {
	chain := m.effectiveChain()
	if len(chain) == 0 {
		m.current.Store(nil)
		return nil
	}

	providers := make([]provider.Provider, 0, len(chain))
	names := make([]string, 0, len(chain))
	disabled := make([]bool, 0, len(chain))
	for _, entry := range chain {
		prov, ep, err := config.ParseChainEntry(entry)
		if err != nil {
			return err
		}
		provCfg, exists := m.cfg.Providers[prov]
		if !exists {
			return fmt.Errorf("manager: chain entry %q: provider missing", entry)
		}
		epCfg, exists := provCfg.Endpoints[ep]
		if !exists {
			return fmt.Errorf("manager: chain entry %q: endpoint missing", entry)
		}
		key := adapterKey(prov, ep)
		adapter, ok := m.adapters[key]
		if !ok {
			built, err := m.adapterBuilder(prov, provCfg, ep, epCfg, m.agent)
			if err != nil {
				return fmt.Errorf("manager: build adapter for %s: %w", entry, err)
			}
			adapter = built
			m.adapters[key] = built
		}
		providers = append(providers, adapter)
		names = append(names, entry)
		// Effective disabled = parent provider disabled OR this
		// endpoint disabled. Either flag alone makes the dispatcher
		// skip the chain entry.
		disabled = append(disabled, provCfg.Disabled || epCfg.Disabled)
	}

	fast, medium, probe := m.policyBuilder(m.cfg.Health)
	fo, err := failover.New(failover.Config{
		Providers:      providers,
		Names:          names,
		Disabled:       disabled,
		CooldownBase:   m.cfg.Health.CooldownBase,
		CooldownMax:    m.cfg.Health.CooldownMax,
		CooldownFactor: m.cfg.Health.CooldownFactor,
		FastPolicy:     fast,
		MediumPolicy:   medium,
		ProbePolicy:    probe,
		Logger:         m.logger,
	})
	if err != nil {
		return err
	}
	m.current.Store(fo)
	return nil
}

func (m *Manager) defaultMaxTokens() int {
	if m.cfg.DefaultMaxTokens > 0 {
		return m.cfg.DefaultMaxTokens
	}
	return 8192
}

// adapterKey produces the cache key for a (provider, endpoint)
// pair. Aligned with the canonical chain ref form so log lines and
// cache keys read the same.
func adapterKey(provider, endpoint string) string {
	return provider + ":" + endpoint
}

// cloneAgentConfig deep-copies AgentConfig so callers can mutate
// the returned value without touching live manager state.
func cloneAgentConfig(a config.AgentConfig) config.AgentConfig {
	out := a
	if a.FallbackChain != nil {
		out.FallbackChain = append([]string(nil), a.FallbackChain...)
	}
	return out
}

// cloneLLMConfig deep-copies LLMConfig (including nested
// Providers.Endpoints maps) so mutations don't bleed across the
// pre-commit working copy and the live snapshot.
func cloneLLMConfig(c config.LLMConfig) config.LLMConfig {
	out := c
	if c.Providers != nil {
		out.Providers = make(map[string]config.ProviderConfig, len(c.Providers))
		for k, v := range c.Providers {
			cp := v
			if v.Endpoints != nil {
				cp.Endpoints = make(map[string]config.EndpointConfig, len(v.Endpoints))
				for ek, ev := range v.Endpoints {
					cp.Endpoints[ek] = ev
				}
			}
			out.Providers[k] = cp
		}
	}
	if c.CustomHeaders != nil {
		out.CustomHeaders = make(map[string]string, len(c.CustomHeaders))
		for k, v := range c.CustomHeaders {
			out.CustomHeaders[k] = v
		}
	}
	return out
}

// effectiveEnabled reports whether (prov, ep) is currently usable
// for routing — i.e. neither the parent provider nor the endpoint
// itself is disabled. Caller holds m.mu.
func (m *Manager) effectiveEnabled(prov, ep string) bool {
	p, ok := m.cfg.Providers[prov]
	if !ok || p.Disabled {
		return false
	}
	e, ok := p.Endpoints[ep]
	return ok && !e.Disabled
}

// autoFillEmptyChain appends "prov:ep" to fallback_chain when (and
// only when) the chain is currently empty AND (prov, ep) is
// effective-enabled. Used right after any mutation that creates or
// re-enables an endpoint, so a degraded-mode server has its first
// usable endpoint promoted into the chain automatically.
//
// Once the chain has any entry, further additions stay manual
// (PUT /fallback-chain) to keep the chain composition explicit.
// Returns true when the chain was modified.
func (m *Manager) autoFillEmptyChain(prov, ep string) bool {
	if len(m.effectiveChain()) != 0 {
		return false
	}
	if !m.effectiveEnabled(prov, ep) {
		return false
	}
	m.setEffectiveChain([]string{config.FormatChainEntry(prov, ep)})
	return true
}

// firstEnabledEndpointName returns the lexicographically smallest
// enabled endpoint name under prov, or "" when none exist.
func (m *Manager) firstEnabledEndpointName(prov string) string {
	p, ok := m.cfg.Providers[prov]
	if !ok || p.Disabled {
		return ""
	}
	names := make([]string, 0, len(p.Endpoints))
	for name, e := range p.Endpoints {
		if !e.Disabled {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}

// filterChainEntry returns a chain with all entries that resolve to
// (prov, ep) removed. Handles both canonical "prov:ep" and legacy
// "prov.ep" forms (via ParseChainEntry). Second return is true
// when any entry was actually removed.
func filterChainEntry(chain []string, prov, ep string) ([]string, bool) {
	out := make([]string, 0, len(chain))
	removed := false
	for _, entry := range chain {
		p, e, err := config.ParseChainEntry(entry)
		if err != nil || (p == prov && e == ep) {
			if err == nil {
				removed = true
			}
			if err != nil {
				// Keep unparseable entries so the next sanitize
				// pass can log + drop them with proper context.
				out = append(out, entry)
			}
			continue
		}
		out = append(out, entry)
	}
	return out, removed
}

// filterChainProvider returns a chain with every entry under prov
// removed. Second return is the number of entries removed.
func filterChainProvider(chain []string, prov string) ([]string, int) {
	out := make([]string, 0, len(chain))
	removed := 0
	for _, entry := range chain {
		p, _, err := config.ParseChainEntry(entry)
		if err != nil {
			out = append(out, entry)
			continue
		}
		if p == prov {
			removed++
			continue
		}
		out = append(out, entry)
	}
	return out, removed
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
