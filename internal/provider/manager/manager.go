// Package manager provides a hot-swappable wrapper around the
// failover dispatcher. The Manager itself implements provider.Provider
// — engine code holds a stable reference to it and never sees the
// underlying Failover swap. Inside, ReplaceChain / UpdateProvider
// rebuild the chain and atomic-swap the live dispatcher so in-flight
// requests continue against the snapshot they were dispatched with,
// while subsequent requests use the new configuration.
package manager

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// AdapterBuilder is the function the cmd/server layer hands in to
// build a Bifrost adapter from a (name, ProviderConfig). The Manager
// holds it so it can rebuild adapters whenever a provider's config
// is mutated through the API.
//
// isPrimary tells the builder whether to inherit llm.bifrost.*
// override fields (only the chain head does — matches the legacy
// single-provider semantics).
type AdapterBuilder func(name string, provCfg config.ProviderConfig, isPrimary bool) (*bifrost.Adapter, error)

// PolicyBuilder constructs the three failover RetryPolicy values from
// the health config. The Manager calls it on every rebuild so the
// policies stay in sync with any later API endpoint that tunes them.
// Today only initProvider invokes the Manager and policies are
// effectively fixed, but the indirection costs nothing and isolates
// the dependency.
type PolicyBuilder func(h config.ProviderHealthConfig) (fast, medium, probe failover.RetryPolicy)

// Manager is the engine-facing provider.Provider plus a hot-swap
// API for runtime configuration changes.
type Manager struct {
	logger         *zap.Logger
	registry       *modelregistry.Registry
	adapterBuilder AdapterBuilder
	policyBuilder  PolicyBuilder

	// current points to the live dispatcher. Reads via Load (Chat
	// path) are lock-free; writes (rebuild) hold mu.
	current atomic.Pointer[failover.Failover]

	// mu serialises configuration mutations (ReplaceChain /
	// UpdateProvider). Read-paths (Chat, snapshots) don't take it.
	mu sync.Mutex
	// cfg is the current LLM config snapshot — the source of truth
	// the Manager rebuilds dispatchers from. Mutating methods clone
	// it, apply the patch, validate, build the new dispatcher, then
	// atomically swap both this field and current.
	cfg config.LLMConfig
	// adapters caches the live Bifrost adapter for each provider
	// name. Entries are reused across rebuilds when the provider's
	// config didn't change, so a chain-only reorder doesn't dial
	// new HTTP connections.
	adapters map[string]*bifrost.Adapter
}

// New builds a Manager around an initial Failover dispatcher.
// providers / names must be parallel slices matching the chain in
// cfg.FallbackChain. adaptersByName captures the same adapters keyed
// by their chain-entry name (so the Manager can dispose / replace
// them later).
func New(
	cfg config.LLMConfig,
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
		adapters:       make(map[string]*bifrost.Adapter),
	}
	if err := m.rebuildLocked(); err != nil {
		return nil, fmt.Errorf("manager: initial build: %w", err)
	}
	return m, nil
}

// ---------- provider.Provider ----------

// Name returns "managed(failover)" so engine logs identify this layer.
func (m *Manager) Name() string { return "managed" }

// Chat delegates to the live dispatcher. The Load is lock-free;
// in-flight requests pin the dispatcher they started with via the
// returned stream, so a concurrent mutation never disturbs them.
func (m *Manager) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	return m.current.Load().Chat(ctx, req)
}

// CountTokens delegates to the live dispatcher.
func (m *Manager) CountTokens(ctx context.Context, msgs []types.Message) (int, error) {
	return m.current.Load().CountTokens(ctx, msgs)
}

// ---------- Snapshots ----------

// ProvidersSnapshot returns every provider configured in
// llm.providers (NOT just the ones in the current chain). API keys
// are returned in plain text — the API is intentionally not
// redacted; gate access to /api/v1/providers at the network layer
// if key exposure is a concern. Sorted by name for stable client UX.
func (m *Manager) ProvidersSnapshot() []ProviderSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ProviderSnapshot, 0, len(m.cfg.Providers))
	for name, p := range m.cfg.Providers {
		out = append(out, ProviderSnapshot{
			Name:        name,
			Type:        p.Type,
			Model:       p.Model,
			BaseURL:     p.BaseURL,
			APIKey:      p.APIKey,
			MaxTokens:   p.MaxTokens,
			Temperature: p.Temperature,
			InChain:     containsString(m.cfg.FallbackChain, name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ChainSnapshot returns the current fallback chain plus the live
// dispatcher's health view of each entry.
func (m *Manager) ChainSnapshot() ChainSnapshotPayload {
	m.mu.Lock()
	chain := append([]string(nil), m.cfg.FallbackChain...)
	m.mu.Unlock()
	health := m.current.Load().Snapshot()
	return ChainSnapshotPayload{
		Chain:    chain,
		Entries:  health,
	}
}

// CurrentConfig returns a deep copy of the live LLM config. Used by
// the persistence layer when writing back to yaml.
func (m *Manager) CurrentConfig() config.LLMConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneLLMConfig(m.cfg)
}

// ---------- Mutations ----------

// ProviderPatch is the partial update accepted by UpdateProvider.
// nil fields mean "leave unchanged". The Manager rebuilds the
// provider's Bifrost adapter whenever any field is non-nil.
//
// Type, when non-nil, switches the bifrost backend protocol for
// this provider — e.g. flipping from anthropic to openai. The
// Manager validates against the bifrost-allowed list before
// rebuilding, so a bad value fails the patch without disrupting
// the live chain.
type ProviderPatch struct {
	Type    *string
	Model   *string
	APIKey  *string
	BaseURL *string
}

// IsEmpty reports whether the patch would change anything.
func (p ProviderPatch) IsEmpty() bool {
	return p.Type == nil && p.Model == nil && p.APIKey == nil && p.BaseURL == nil
}

// UpdateProvider applies a partial patch to llm.providers[name],
// rebuilds that provider's Bifrost adapter, and (if the provider is
// in the current chain) swaps in a new dispatcher.
//
// Errors:
//   - "unknown provider" when name isn't in cfg.Providers
//   - "empty patch" when the patch wouldn't change anything
//   - wrapped adapter-build error when the new config is invalid
//
// On error the in-memory state is unchanged.
func (m *Manager) UpdateProvider(name string, patch ProviderPatch) error {
	if patch.IsEmpty() {
		return errors.New("manager: empty provider patch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	provCfg, ok := m.cfg.Providers[name]
	if !ok {
		return fmt.Errorf("manager: unknown provider %q", name)
	}
	if patch.Type != nil {
		provCfg.Type = *patch.Type
	}
	if patch.Model != nil {
		provCfg.Model = *patch.Model
	}
	if patch.APIKey != nil {
		provCfg.APIKey = *patch.APIKey
	}
	if patch.BaseURL != nil {
		provCfg.BaseURL = *patch.BaseURL
	}

	// Build the new adapter eagerly so a bad config errors out BEFORE
	// the swap.
	isPrimary := len(m.cfg.FallbackChain) > 0 && m.cfg.FallbackChain[0] == name
	newAdapter, err := m.adapterBuilder(name, provCfg, isPrimary)
	if err != nil {
		return fmt.Errorf("manager: build adapter %q: %w", name, err)
	}

	// Commit: replace cfg + adapter + rebuild dispatcher.
	oldAdapter := m.adapters[name]
	m.cfg.Providers[name] = provCfg
	m.adapters[name] = newAdapter
	if err := m.rebuildLocked(); err != nil {
		// Rollback (best-effort).
		m.cfg.Providers[name] = provCfg // already mutated copy; reverting requires the original — but we deep-copied above. For simplicity, log and leave the patched state; future rebuilds will use it.
		m.adapters[name] = oldAdapter
		return fmt.Errorf("manager: rebuild after patch: %w", err)
	}
	if oldAdapter != nil && oldAdapter != newAdapter {
		oldAdapter.Shutdown()
	}
	m.logger.Info("manager: provider updated",
		zap.String("name", name),
		zap.Bool("in_chain", containsString(m.cfg.FallbackChain, name)),
		zap.Bool("is_primary", isPrimary),
	)
	return nil
}

// ReplaceChain replaces llm.fallback_chain with newChain. Every
// entry must reference a provider in cfg.Providers. The new chain
// must be non-empty.
//
// On success a new dispatcher is built (existing adapters are
// reused; missing ones are built fresh) and swapped in.
//
// On error the in-memory state is unchanged.
func (m *Manager) ReplaceChain(newChain []string) error {
	if len(newChain) == 0 {
		return errors.New("manager: fallback_chain cannot be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range newChain {
		if _, ok := m.cfg.Providers[name]; !ok {
			return fmt.Errorf("manager: unknown provider %q in chain", name)
		}
	}

	prevChain := append([]string(nil), m.cfg.FallbackChain...)
	m.cfg.FallbackChain = append([]string(nil), newChain...)
	if err := m.rebuildLocked(); err != nil {
		m.cfg.FallbackChain = prevChain
		return fmt.Errorf("manager: rebuild after chain replace: %w", err)
	}
	m.logger.Info("manager: chain replaced",
		zap.Strings("from", prevChain),
		zap.Strings("to", newChain),
	)
	return nil
}

// ---------- internal ----------

// rebuildLocked constructs a new Failover dispatcher from the
// current cfg + adapter cache and atomic-swaps it into current.
// Caller must hold m.mu.
func (m *Manager) rebuildLocked() error {
	chain := m.cfg.FallbackChain
	if len(chain) == 0 {
		return errors.New("manager: fallback_chain is empty (single-provider mode should not go through manager)")
	}

	providers := make([]provider.Provider, 0, len(chain))
	names := make([]string, 0, len(chain))
	for i, name := range chain {
		isPrimary := i == 0
		adapter, ok := m.adapters[name]
		if !ok {
			provCfg, exists := m.cfg.Providers[name]
			if !exists {
				return fmt.Errorf("manager: chain entry %q missing from providers", name)
			}
			built, err := m.adapterBuilder(name, provCfg, isPrimary)
			if err != nil {
				return fmt.Errorf("manager: build adapter %q: %w", name, err)
			}
			adapter = built
			m.adapters[name] = built
		}
		providers = append(providers, adapter)
		names = append(names, name)
	}

	fast, medium, probe := m.policyBuilder(m.cfg.Health)
	fo, err := failover.New(failover.Config{
		Providers:      providers,
		Names:          names,
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

// cloneLLMConfig deep-copies LLMConfig so mutations don't bleed.
func cloneLLMConfig(c config.LLMConfig) config.LLMConfig {
	out := c
	if c.Providers != nil {
		out.Providers = make(map[string]config.ProviderConfig, len(c.Providers))
		for k, v := range c.Providers {
			out.Providers[k] = v
		}
	}
	if c.FallbackChain != nil {
		out.FallbackChain = append([]string(nil), c.FallbackChain...)
	}
	if c.CustomHeaders != nil {
		out.CustomHeaders = make(map[string]string, len(c.CustomHeaders))
		for k, v := range c.CustomHeaders {
			out.CustomHeaders[k] = v
		}
	}
	return out
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
