package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/provider/failover"
)

// fakeBuilder hands out a deterministic Bifrost adapter for any
// (provider, endpoint) pair, and records build calls so tests can
// assert reuse / rebuild.
type fakeBuilder struct {
	mu       sync.Mutex
	calls    map[string]int // key: "provider.endpoint"
	errOnKey string         // simulate adapter-build failure for this key
	adapters map[string]*bifrost.Adapter
}

func newFakeBuilder() *fakeBuilder {
	return &fakeBuilder{
		calls:    map[string]int{},
		adapters: map[string]*bifrost.Adapter{},
	}
}

func (f *fakeBuilder) build(provName string, _ config.ProviderConfig, epName string, _ config.EndpointConfig, _ config.AgentConfig) (*bifrost.Adapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := provName + ":" + epName
	f.calls[key]++
	if key == f.errOnKey {
		return nil, errors.New("simulated adapter build failure")
	}
	if a, ok := f.adapters[key]; ok {
		return a, nil
	}
	a := &bifrost.Adapter{}
	f.adapters[key] = a
	return a, nil
}

func (f *fakeBuilder) callCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func defaultPolicyBuilder(_ config.ProviderHealthConfig) (failover.RetryPolicy, failover.RetryPolicy, failover.RetryPolicy) {
	return failover.FastPolicy, failover.MediumPolicy, failover.ProbePolicy
}

func baseCfg() config.LLMConfig {
	return config.LLMConfig{
		Providers: map[string]config.ProviderConfig{
			"alpha": {
				Type:    "anthropic",
				BaseURL: "https://a.example",
				APIKey:  "sk-aaaaaaaaaaaaaaaa",
				Endpoints: map[string]config.EndpointConfig{
					"claude-46": {Model: "claude-sonnet-4-6", MaxTokens: 16384},
				},
			},
			"beta": {
				Type:    "openai",
				BaseURL: "https://b.example",
				APIKey:  "sk-bbbbbbbbbbbbbbbb",
				Endpoints: map[string]config.EndpointConfig{
					"gpt-5": {Model: "gpt-5-turbo", MaxTokens: 4096},
				},
			},
		},
		Health: config.ProviderHealthConfig{
			CooldownBase:   30 * time.Second,
			CooldownMax:    5 * time.Minute,
			CooldownFactor: 2,
		},
	}
}

// baseAgent returns the default agent config used by most tests
// (alpha:claude-46 primary, beta:gpt-5 as the only fallback).
func baseAgent() config.AgentConfig {
	return config.AgentConfig{
		Primary:       "alpha:claude-46",
		FallbackChain: []string{"beta:gpt-5"},
	}
}

// agentFromChain converts a flat effective chain into AgentConfig
// (chain[0] → primary, chain[1:] → fallback). Lets test cases
// continue describing scenarios as a single ordered list.
func agentFromChain(chain ...string) config.AgentConfig {
	if len(chain) == 0 {
		return config.AgentConfig{}
	}
	a := config.AgentConfig{Primary: chain[0]}
	if len(chain) > 1 {
		a.FallbackChain = append([]string(nil), chain[1:]...)
	}
	return a
}

// effectiveChain reads the live agent snapshot and returns the
// dispatcher-order chain ([primary, ...fallback_chain] deduplicated).
func effectiveChain(m *Manager) []string {
	snap := m.AgentSnapshot()
	out := make([]string, 0, 1+len(snap.FallbackChain))
	seen := map[string]bool{}
	if snap.Primary != "" {
		out = append(out, snap.Primary)
		seen[snap.Primary] = true
	}
	for _, e := range snap.FallbackChain {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func mustNewManager(t *testing.T, cfg config.LLMConfig, agent config.AgentConfig, fb *fakeBuilder) *Manager {
	t.Helper()
	m, err := New(cfg, agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	return m
}

// TestNew_EmptyChainDegradedMode verifies Manager constructs OK
// with no primary AND empty fallback (Chat returns ErrNoEndpoint,
// but the management API stays mountable so operators can recover
// via PATCH /api/v1/agent).
func TestNew_EmptyChainDegradedMode(t *testing.T) {
	fb := newFakeBuilder()
	m, err := New(baseCfg(), config.AgentConfig{}, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatalf("New with empty agent should succeed; got %v", err)
	}
	if _, err := m.Chat(context.Background(), nil); err != ErrNoEndpoint {
		t.Fatalf("Chat in degraded mode err = %v, want ErrNoEndpoint", err)
	}
	if got := effectiveChain(m); len(got) != 0 {
		t.Fatalf("degraded effective chain should be empty, got %v", got)
	}
	// Recovery: populate the chain via API.
	if err := m.ReplaceChain([]string{"alpha:claude-46"}); err != nil {
		t.Fatalf("ReplaceChain from empty → populated: %v", err)
	}
	if got := effectiveChain(m); len(got) != 1 || got[0] != "alpha:claude-46" {
		t.Fatalf("after recovery chain = %v", got)
	}
}

func TestNew_BuildsInitialChain(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if fb.callCount("alpha:claude-46") != 1 || fb.callCount("beta:gpt-5") != 1 {
		t.Fatalf("expected one build per chain entry; got alpha=%d beta=%d",
			fb.callCount("alpha:claude-46"), fb.callCount("beta:gpt-5"))
	}
	got := effectiveChain(m)
	if len(got) != 2 || got[0] != "alpha:claude-46" || got[1] != "beta:gpt-5" {
		t.Fatalf("effective chain order = %v", got)
	}
}

func TestReplaceChain_ReusesAdapters(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.ReplaceChain([]string{"beta:gpt-5", "alpha:claude-46"}); err != nil {
		t.Fatalf("ReplaceChain err = %v", err)
	}
	if fb.callCount("alpha:claude-46") != 1 || fb.callCount("beta:gpt-5") != 1 {
		t.Fatalf("reorder should reuse adapters; got %v", fb.calls)
	}
}

func TestReplaceChain_RejectsMalformed(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.ReplaceChain([]string{"missing-separator"}); err == nil {
		t.Fatalf("expected error for entry without separator")
	}
	if err := m.ReplaceChain([]string{"ghost:x"}); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
	if err := m.ReplaceChain([]string{"alpha:ghost"}); err == nil {
		t.Fatalf("expected error for unknown endpoint")
	}
}

// TestReplaceChain_BackwardCompatibleDotSeparator confirms legacy
// "provider.endpoint" inputs still parse so older yaml / clients
// don't break on upgrade.
func TestReplaceChain_BackwardCompatibleDotSeparator(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.ReplaceChain([]string{"alpha.claude-46", "beta.gpt-5"}); err != nil {
		t.Fatalf("dot-separator legacy form should still parse, got %v", err)
	}
	got := effectiveChain(m)
	if len(got) != 2 || got[0] != "alpha.claude-46" || got[1] != "beta.gpt-5" {
		t.Fatalf("chain not preserved as-typed: %v", got)
	}
}

// TestEndpointNameMayContainDot covers the original motivation for
// the ':' separator: endpoint names like "gpt-5.5" or
// "claude-3.5-sonnet" must be expressible in chain refs.
func TestEndpointNameMayContainDot(t *testing.T) {
	cfg := baseCfg()
	cfg.Providers["openai-new"] = config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://api.openai.com",
		APIKey:  "sk-openai",
		Endpoints: map[string]config.EndpointConfig{
			"gpt-5.5": {Model: "gpt-5.5", MaxTokens: 4096},
		},
	}
	agent := agentFromChain("openai-new:gpt-5.5", "alpha:claude-46")

	fb := newFakeBuilder()
	m, err := New(cfg, agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatalf("New with dot-in-endpoint name: %v", err)
	}
	got := effectiveChain(m)
	if got[0] != "openai-new:gpt-5.5" {
		t.Fatalf("chain[0] = %q, want openai-new:gpt-5.5", got[0])
	}
}

func TestUpdateProviderCreds_RebuildsAllEndpointAdapters(t *testing.T) {
	// alpha has only one endpoint in baseCfg — add another so we can
	// see "N endpoints rebuilt".
	cfg := baseCfg()
	alpha := cfg.Providers["alpha"]
	alpha.Endpoints["claude-45"] = config.EndpointConfig{Model: "claude-sonnet-4-5", MaxTokens: 16384}
	cfg.Providers["alpha"] = alpha
	agent := agentFromChain("alpha:claude-46", "alpha:claude-45", "beta:gpt-5")

	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, agent, fb)
	preAlpha46 := fb.callCount("alpha:claude-46")
	preAlpha45 := fb.callCount("alpha:claude-45")
	preBeta := fb.callCount("beta:gpt-5")

	newKey := "sk-alpha-NEW"
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{APIKey: &newKey}); err != nil {
		t.Fatalf("UpdateProviderCreds err = %v", err)
	}
	if fb.callCount("alpha:claude-46") != preAlpha46+1 {
		t.Errorf("alpha.claude-46 rebuilt %d times, want +1", fb.callCount("alpha:claude-46")-preAlpha46)
	}
	if fb.callCount("alpha:claude-45") != preAlpha45+1 {
		t.Errorf("alpha.claude-45 rebuilt %d times, want +1", fb.callCount("alpha:claude-45")-preAlpha45)
	}
	if fb.callCount("beta:gpt-5") != preBeta {
		t.Errorf("beta.gpt-5 should NOT have been rebuilt; got %d (was %d)", fb.callCount("beta:gpt-5"), preBeta)
	}
}

func TestAddProvider_CreatesEmpty(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	err := m.AddProvider("deepseek", config.ProviderConfig{
		Type:    "openai",
		BaseURL: "https://api.deepseek.com",
		APIKey:  "sk-xxx",
	})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	var found bool
	for _, p := range m.ProvidersSnapshot() {
		if p.Name == "deepseek" {
			found = true
			if p.Type != "openai" || p.BaseURL != "https://api.deepseek.com" {
				t.Errorf("snapshot mismatch: %+v", p)
			}
			if len(p.Endpoints) != 0 {
				t.Errorf("new provider should have no endpoints, got %v", p.Endpoints)
			}
		}
	}
	if !found {
		t.Fatalf("deepseek not in snapshot")
	}
}

func TestAddProvider_RejectsBadName(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	for _, bad := range []string{"", "bad.name", "bad:name"} {
		if err := m.AddProvider(bad, config.ProviderConfig{Type: "openai"}); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}

func TestAddProvider_RejectsUnknownType(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.AddProvider("kimi", config.ProviderConfig{Type: "kimi"}); err == nil {
		t.Fatalf("unknown type should be rejected")
	}
}

func TestAddProvider_RejectsDuplicate(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.AddProvider("alpha", config.ProviderConfig{Type: "openai"}); err == nil {
		t.Fatalf("duplicate provider should be rejected")
	}
}

func TestAddProvider_AcceptsGemini(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	if err := m.AddProvider("google", config.ProviderConfig{
		Type:    "gemini",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta",
		APIKey:  "key",
	}); err != nil {
		t.Fatalf("gemini should now be allowed; got %v", err)
	}
}

func TestAddEndpoint_NotInChainByDefault(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	err := m.AddEndpoint("alpha", "claude-45", config.EndpointConfig{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 16384,
	})
	if err != nil {
		t.Fatalf("AddEndpoint err = %v", err)
	}
	// New endpoint exists.
	var found bool
	for _, p := range m.ProvidersSnapshot() {
		if p.Name != "alpha" {
			continue
		}
		for _, e := range p.Endpoints {
			if e.Name == "claude-45" {
				found = true
				if e.InChain {
					t.Errorf("new endpoint should not be auto-added to chain")
				}
			}
		}
	}
	if !found {
		t.Fatalf("claude-45 not in snapshot")
	}
}

func TestAddEndpoint_DefaultsMaxTokens(t *testing.T) {
	cfg := baseCfg()
	cfg.DefaultMaxTokens = 4096
	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, baseAgent(), fb)
	if err := m.AddEndpoint("alpha", "claude-45", config.EndpointConfig{
		Model: "claude-sonnet-4-5",
		// MaxTokens omitted
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	for _, p := range m.ProvidersSnapshot() {
		for _, e := range p.Endpoints {
			if e.Name == "claude-45" && e.MaxTokens != 4096 {
				t.Fatalf("MaxTokens = %d, want default 4096", e.MaxTokens)
			}
		}
	}
}

// TestDisableProvider_CascadesToAllEndpoints verifies that toggling
// disabled=true at the PROVIDER level disables every endpoint under
// it in the chain, regardless of per-endpoint disabled flag.
func TestDisableProvider_CascadesToAllEndpoints(t *testing.T) {
	// Set up: 2 endpoints under alpha both in chain.
	cfg := baseCfg()
	alpha := cfg.Providers["alpha"]
	alpha.Endpoints["claude-45"] = config.EndpointConfig{Model: "c45", MaxTokens: 16384}
	cfg.Providers["alpha"] = alpha
	agent := agentFromChain("alpha:claude-46", "alpha:claude-45", "beta:gpt-5")

	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, agent, fb)

	// Snapshot baseline: nothing disabled.
	for _, e := range m.AgentSnapshot().Entries {
		if e.Disabled {
			t.Fatalf("baseline: chain entry %s.%s should not be disabled", e.Provider, e.Endpoint)
		}
	}

	// Disable alpha at provider level.
	disable := true
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &disable}); err != nil {
		t.Fatalf("UpdateProviderCreds disable: %v", err)
	}

	// All remaining chain entries (now beta:gpt-5 only after auto-removal)
	// stay enabled; auto-removal stripped alpha.* entries from chain.
	for _, e := range m.AgentSnapshot().Entries {
		if e.Provider == "alpha" && !e.Disabled {
			t.Errorf("chain entry %s.%s disabled=%v, want true", e.Provider, e.Endpoint, e.Disabled)
		}
	}

	// Provider snapshot also reports it.
	for _, p := range m.ProvidersSnapshot() {
		if p.Name == "alpha" && !p.Disabled {
			t.Errorf("alpha provider snapshot missing disabled flag")
		}
		if p.Name == "beta" && p.Disabled {
			t.Errorf("beta provider should not be disabled")
		}
	}

	// Re-enable.
	enable := false
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &enable}); err != nil {
		t.Fatalf("re-enable alpha: %v", err)
	}
	for _, e := range m.AgentSnapshot().Entries {
		if e.Disabled {
			t.Errorf("after re-enable: chain entry %s.%s should not be disabled", e.Provider, e.Endpoint)
		}
	}
}

// TestAddEndpoint_AutoFillsEmptyChain confirms that POSTing a new
// endpoint when the chain is empty promotes it to primary.
func TestAddEndpoint_AutoFillsEmptyChain(t *testing.T) {
	fb := newFakeBuilder()
	m, err := New(baseCfg(), config.AgentConfig{}, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddEndpoint("alpha", "claude-haiku", config.EndpointConfig{
		Model: "claude-3-5-haiku", MaxTokens: 8192,
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	got := effectiveChain(m)
	if len(got) != 1 || got[0] != "alpha:claude-haiku" {
		t.Fatalf("chain = %v, want [alpha:claude-haiku]", got)
	}
}

// TestAddEndpoint_DoesNotTouchNonEmptyChain confirms a subsequent
// POST does NOT auto-add when the chain is already populated.
func TestAddEndpoint_DoesNotTouchNonEmptyChain(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	if err := m.AddEndpoint("alpha", "claude-haiku", config.EndpointConfig{
		Model: "claude-3-5-haiku", MaxTokens: 8192,
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	got := effectiveChain(m)
	for _, c := range got {
		if c == "alpha:claude-haiku" {
			t.Fatalf("new endpoint should NOT auto-add when chain non-empty: %v", got)
		}
	}
}

// TestAddEndpoint_DisabledNotAutoAdded confirms creating a
// disabled-from-birth endpoint never auto-fills chain.
func TestAddEndpoint_DisabledNotAutoAdded(t *testing.T) {
	fb := newFakeBuilder()
	m, err := New(baseCfg(), config.AgentConfig{}, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddEndpoint("alpha", "claude-haiku", config.EndpointConfig{
		Model: "claude-3-5-haiku", MaxTokens: 8192, Disabled: true,
	}); err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	if got := effectiveChain(m); len(got) != 0 {
		t.Fatalf("disabled new endpoint should not auto-fill chain: %v", got)
	}
}

// TestEnableEndpoint_AutoFillsEmptyChain: PATCH disabled=false on
// the only endpoint after chain was emptied → re-promote.
func TestEnableEndpoint_AutoFillsEmptyChain(t *testing.T) {
	agent := agentFromChain("alpha:claude-46")
	fb := newFakeBuilder()
	m, err := New(baseCfg(), agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	disable := true
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Disabled: &disable}); err != nil {
		t.Fatal(err)
	}
	if got := effectiveChain(m); len(got) != 0 {
		t.Fatalf("after disable, chain should be empty, got %v", got)
	}
	// Now re-enable — auto-fill since chain is empty.
	enable := false
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Disabled: &enable}); err != nil {
		t.Fatal(err)
	}
	got := effectiveChain(m)
	if len(got) != 1 || got[0] != "alpha:claude-46" {
		t.Fatalf("after re-enable with empty chain, expected auto-fill: chain = %v", got)
	}
}

// TestEnableProvider_AutoFillsEmptyChain: PATCH provider disabled=false
// on a provider whose first enabled endpoint should auto-fill.
func TestEnableProvider_AutoFillsEmptyChain(t *testing.T) {
	cfg := baseCfg()
	// Start with the alpha provider disabled and chain empty.
	a := cfg.Providers["alpha"]
	a.Disabled = true
	cfg.Providers["alpha"] = a
	fb := newFakeBuilder()
	m, err := New(cfg, config.AgentConfig{}, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	// Re-enable alpha; baseCfg has 1 endpoint claude-46 (enabled),
	// chain should auto-fill with alpha:claude-46.
	enable := false
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &enable}); err != nil {
		t.Fatal(err)
	}
	got := effectiveChain(m)
	if len(got) != 1 || got[0] != "alpha:claude-46" {
		t.Fatalf("expected auto-fill alpha:claude-46, got %v", got)
	}
}

// TestDisableEndpoint_AutoRemovesFromChain checks that PATCH
// disabled=true on an in-chain endpoint also removes the entry
// from the agent chain (primary → cleared, or fallback entry removed).
func TestDisableEndpoint_AutoRemovesFromChain(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	// chain initially has alpha:claude-46 + beta:gpt-5
	disable := true
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Disabled: &disable}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got := effectiveChain(m)
	if len(got) != 1 || got[0] != "beta:gpt-5" {
		t.Fatalf("after disabling alpha:claude-46, chain = %v, want [beta:gpt-5]", got)
	}

	// Re-enable does NOT auto-add back.
	enable := false
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Disabled: &enable}); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	got = effectiveChain(m)
	if len(got) != 1 || got[0] != "beta:gpt-5" {
		t.Fatalf("after re-enable, chain should NOT auto-restore: got %v", got)
	}
}

// TestDisableProvider_AutoRemovesAllChainEntries checks that
// PATCH disabled=true on a provider removes every chain entry
// referencing it.
func TestDisableProvider_AutoRemovesAllChainEntries(t *testing.T) {
	cfg := baseCfg()
	alpha := cfg.Providers["alpha"]
	alpha.Endpoints["claude-45"] = config.EndpointConfig{Model: "c45", MaxTokens: 16384}
	cfg.Providers["alpha"] = alpha
	agent := agentFromChain("alpha:claude-46", "alpha:claude-45", "beta:gpt-5")
	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, agent, fb)

	disable := true
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &disable}); err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	got := effectiveChain(m)
	if len(got) != 1 || got[0] != "beta:gpt-5" {
		t.Fatalf("after disabling alpha, chain = %v, want [beta:gpt-5]", got)
	}
}

// TestDisableEndpoint_CanEmptyChain confirms disabling the only
// chain entry empties the chain (Manager enters degraded mode,
// not an error).
func TestDisableEndpoint_CanEmptyChain(t *testing.T) {
	agent := agentFromChain("alpha:claude-46")
	fb := newFakeBuilder()
	m, err := New(baseCfg(), agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	disable := true
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Disabled: &disable}); err != nil {
		t.Fatalf("disable only chain entry should succeed (enters degraded mode), got %v", err)
	}
	if got := effectiveChain(m); len(got) != 0 {
		t.Fatalf("chain should be empty after disabling only entry, got %v", got)
	}
}

// TestDisableProvider_OrWithEndpointFlag confirms effective disabled
// is provider.Disabled OR endpoint.Disabled — once provider is
// re-enabled, an endpoint that had its own Disabled=true stays
// disabled.
func TestDisableProvider_OrWithEndpointFlag(t *testing.T) {
	cfg := baseCfg()
	// alpha.claude-46 starts disabled at endpoint level
	a := cfg.Providers["alpha"]
	a.Endpoints["claude-46"] = config.EndpointConfig{Model: "c46", MaxTokens: 16384, Disabled: true}
	cfg.Providers["alpha"] = a

	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, baseAgent(), fb)

	// Provider alpha not disabled at provider level; but endpoint is.
	for _, e := range m.AgentSnapshot().Entries {
		if e.Provider == "alpha" && e.Endpoint == "claude-46" && !e.Disabled {
			t.Errorf("endpoint with own Disabled=true should be disabled")
		}
	}

	// Disable alpha at provider level too — still disabled (OR semantics).
	disable := true
	_ = m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &disable})
	for _, e := range m.AgentSnapshot().Entries {
		if e.Provider == "alpha" && !e.Disabled {
			t.Errorf("provider-level disable should override")
		}
	}

	// Re-enable provider — but endpoint flag still applies.
	enable := false
	_ = m.UpdateProviderCreds("alpha", ProviderCredsPatch{Disabled: &enable})
	for _, e := range m.AgentSnapshot().Entries {
		if e.Provider == "alpha" && e.Endpoint == "claude-46" && !e.Disabled {
			t.Errorf("endpoint flag should persist after provider re-enable")
		}
	}
}

// TestYamlConfiguredDisabledEndpoint_ShownButSkipped covers the
// case where yaml explicitly sets disabled=true AND keeps the entry
// in fallback_chain (i.e. operator hand-edited). ChainSnapshot
// reflects the disabled flag and dispatcher skips it.
//
// (PATCH-driven disable auto-removes from chain — see
// TestDisableEndpoint_AutoRemovesFromChain.)
func TestYamlConfiguredDisabledEndpoint_ShownButSkipped(t *testing.T) {
	cfg := baseCfg()
	a := cfg.Providers["alpha"]
	a.Endpoints["claude-46"] = config.EndpointConfig{
		Model: "claude-sonnet-4-6", MaxTokens: 16384, Disabled: true,
	}
	cfg.Providers["alpha"] = a
	// chain still references the disabled endpoint (simulating
	// operator who left it in yaml on purpose).
	agent := agentFromChain("alpha:claude-46", "beta:gpt-5")

	fb := newFakeBuilder()
	m, err := New(cfg, agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	snap := m.AgentSnapshot()
	if len(snap.Entries) != 2 {
		t.Fatalf("expected 2 chain entries, got %d", len(snap.Entries))
	}
	if !snap.Entries[0].Disabled {
		t.Errorf("yaml-configured disabled entry should report Disabled=true: %+v", snap.Entries[0])
	}
	if snap.Entries[1].Disabled {
		t.Errorf("chain[1] should not be disabled: %+v", snap.Entries[1])
	}
}

// TestAddEndpoint_WithDisabled covers POST endpoint with disabled=true.
func TestAddEndpoint_WithDisabled(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	err := m.AddEndpoint("alpha", "claude-45", config.EndpointConfig{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 16384,
		Disabled:  true,
	})
	if err != nil {
		t.Fatalf("AddEndpoint: %v", err)
	}
	for _, p := range m.ProvidersSnapshot() {
		if p.Name == "alpha" {
			for _, e := range p.Endpoints {
				if e.Name == "claude-45" && !e.Disabled {
					t.Errorf("new endpoint should be disabled, got %+v", e)
				}
			}
		}
	}
}

func TestDeleteEndpoint_AutoRemovesFromChain(t *testing.T) {
	cfg := baseCfg()
	// Add claude-45 + put it in chain
	alpha := cfg.Providers["alpha"]
	alpha.Endpoints["claude-45"] = config.EndpointConfig{Model: "c45", MaxTokens: 16384}
	cfg.Providers["alpha"] = alpha
	agent := agentFromChain("alpha:claude-46", "alpha:claude-45", "beta:gpt-5")

	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, agent, fb)
	if err := m.DeleteEndpoint("alpha", "claude-45"); err != nil {
		t.Fatalf("DeleteEndpoint err = %v", err)
	}
	got := effectiveChain(m)
	for _, c := range got {
		if c == "alpha:claude-45" {
			t.Fatalf("deleted endpoint still in chain: %v", got)
		}
	}
}

func TestDeleteEndpoint_RejectsWhenItsTheLastChainEntry(t *testing.T) {
	agent := agentFromChain("alpha:claude-46") // single entry
	fb := newFakeBuilder()
	m, err := New(baseCfg(), agent, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteEndpoint("alpha", "claude-46"); err == nil {
		t.Fatalf("expected refusal: deleting only chain entry would empty the chain")
	}
}

func TestUpdateEndpoint(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	newModel := "claude-sonnet-4-6-v2"
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Model: &newModel}); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	for _, p := range m.ProvidersSnapshot() {
		if p.Name == "alpha" {
			for _, e := range p.Endpoints {
				if e.Name == "claude-46" && e.Model != "claude-sonnet-4-6-v2" {
					t.Fatalf("model = %q, want claude-sonnet-4-6-v2", e.Model)
				}
			}
		}
	}
}

func TestUpdateProviderCreds_UnknownTypeRejected(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	bad := "kimi"
	if err := m.UpdateProviderCreds("alpha", ProviderCredsPatch{Type: &bad}); err == nil {
		t.Fatalf("expected error for unknown type")
	}
}

func TestProvidersSnapshot_ReturnsAPIKeyVerbatim(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	want := map[string]string{
		"alpha": "sk-aaaaaaaaaaaaaaaa",
		"beta":  "sk-bbbbbbbbbbbbbbbb",
	}
	for _, p := range m.ProvidersSnapshot() {
		if p.APIKey != want[p.Name] {
			t.Fatalf("provider %s: api_key = %q, want %q", p.Name, p.APIKey, want[p.Name])
		}
	}
}

func TestChat_DelegatesToCurrent_ConcurrentSafe(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)
	var wg sync.WaitGroup
	var crashes atomic.Int32
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					crashes.Add(1)
				}
			}()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if fo := m.current.Load(); fo == nil {
					crashes.Add(1)
					return
				}
				_ = m.AgentSnapshot()
			}
		}()
	}
	for i := 0; i < 20; i++ {
		next := []string{"alpha:claude-46", "beta:gpt-5"}
		if i%2 == 1 {
			next = []string{"beta:gpt-5", "alpha:claude-46"}
		}
		if err := m.ReplaceChain(next); err != nil {
			t.Fatalf("ReplaceChain err = %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if crashes.Load() > 0 {
		t.Fatalf("%d concurrent crashes during hot swap", crashes.Load())
	}
}

var _ provider.Provider = (*Manager)(nil)
var _ = context.Background

// TestEffectiveContextWindow_PrimaryEndpointCaps covers the table of agent×endpoint combinations.
func TestEffectiveContextWindow_PrimaryEndpointCaps(t *testing.T) {
	cases := []struct {
		name    string
		agentCW int
		epCW    int
		want    int
	}{
		{"both_unset_fallback", 0, 0, 200000},
		{"agent_only", 100000, 0, 100000},
		{"endpoint_only", 0, 128000, 128000},
		{"agent_under_endpoint_uses_agent", 64000, 128000, 64000},
		{"agent_over_endpoint_uses_endpoint", 500000, 200000, 200000},
		{"agent_equals_endpoint", 200000, 200000, 200000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := baseCfg()
			alpha := cfg.Providers["alpha"]
			ep := alpha.Endpoints["claude-46"]
			ep.ContextWindow = c.epCW
			alpha.Endpoints["claude-46"] = ep
			cfg.Providers["alpha"] = alpha

			agent := baseAgent()
			agent.ContextWindow = c.agentCW

			fb := newFakeBuilder()
			m := mustNewManager(t, cfg, agent, fb)
			got := m.EffectiveContextWindow()
			if got != c.want {
				t.Errorf("EffectiveContextWindow() = %d, want %d", got, c.want)
			}
		})
	}
}

// TestAgentSnapshot_CarriesEffectiveContextWindow confirms the snapshot exposes BOTH the raw
// configured value AND the effective (capped) value.
func TestAgentSnapshot_CarriesEffectiveContextWindow(t *testing.T) {
	cfg := baseCfg()
	alpha := cfg.Providers["alpha"]
	ep := alpha.Endpoints["claude-46"]
	ep.ContextWindow = 150000
	alpha.Endpoints["claude-46"] = ep
	cfg.Providers["alpha"] = alpha

	agent := baseAgent()
	agent.ContextWindow = 400000 // exceeds endpoint cap

	fb := newFakeBuilder()
	m := mustNewManager(t, cfg, agent, fb)
	snap := m.AgentSnapshot()

	if snap.ContextWindow != 400000 {
		t.Errorf("snap.ContextWindow = %d (raw configured), want 400000", snap.ContextWindow)
	}
	if snap.EffectiveContextWindow != 150000 {
		t.Errorf("snap.EffectiveContextWindow = %d (capped), want 150000", snap.EffectiveContextWindow)
	}
}

func TestUpdateEndpoint_GroupRoundTrip(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), baseAgent(), fb)

	// Set group
	grp := "Claude-4"
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Group: &grp}); err != nil {
		t.Fatalf("set group: %v", err)
	}
	found := false
	for _, p := range m.ProvidersSnapshot() {
		if p.Name != "alpha" {
			continue
		}
		for _, e := range p.Endpoints {
			if e.Name != "claude-46" {
				continue
			}
			found = true
			if e.Group != "Claude-4" {
				t.Fatalf("after set: group = %q, want Claude-4", e.Group)
			}
		}
	}
	if !found {
		t.Fatal("after set: alpha:claude-46 not found in snapshot")
	}

	// Clear group via explicit empty string
	empty := ""
	if err := m.UpdateEndpoint("alpha", "claude-46", EndpointPatch{Group: &empty}); err != nil {
		t.Fatalf("clear group: %v", err)
	}
	found = false
	for _, p := range m.ProvidersSnapshot() {
		if p.Name != "alpha" {
			continue
		}
		for _, e := range p.Endpoints {
			if e.Name != "claude-46" {
				continue
			}
			found = true
			if e.Group != "" {
				t.Fatalf("after clear: group = %q, want \"\"", e.Group)
			}
		}
	}
	if !found {
		t.Fatal("after clear: alpha:claude-46 not found in snapshot")
	}
}

func TestEndpointPatch_GroupAloneIsNotEmpty(t *testing.T) {
	grp := "X"
	patch := EndpointPatch{Group: &grp}
	if patch.IsEmpty() {
		t.Error("EndpointPatch with only Group=&\"X\" must not be empty")
	}
	empty := ""
	patch = EndpointPatch{Group: &empty}
	if patch.IsEmpty() {
		t.Error("EndpointPatch with only Group=&\"\" (explicit clear) must not be empty")
	}
}
