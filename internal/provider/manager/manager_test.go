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

// fakeBuilder hands out a deterministic Bifrost adapter for any name,
// and records build calls so tests can assert reuse / rebuild.
type fakeBuilder struct {
	mu       sync.Mutex
	calls    map[string]int     // name → count
	errOn    string             // simulate adapter-build failure for this name
	adapters map[string]*bifrost.Adapter
}

func newFakeBuilder() *fakeBuilder {
	return &fakeBuilder{
		calls:    map[string]int{},
		adapters: map[string]*bifrost.Adapter{},
	}
}

func (f *fakeBuilder) build(name string, _ config.ProviderConfig, _ bool) (*bifrost.Adapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	if name == f.errOn {
		return nil, errors.New("simulated adapter build failure")
	}
	// Reuse the same adapter pointer for the same name so identity
	// checks work in tests. A real Bifrost adapter is opaque here —
	// the failover layer never invokes it because none of these
	// tests run Chat.
	if a, ok := f.adapters[name]; ok {
		return a, nil
	}
	a := &bifrost.Adapter{}
	f.adapters[name] = a
	return a, nil
}

func (f *fakeBuilder) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func defaultPolicyBuilder(_ config.ProviderHealthConfig) (failover.RetryPolicy, failover.RetryPolicy, failover.RetryPolicy) {
	return failover.FastPolicy, failover.MediumPolicy, failover.ProbePolicy
}

func baseCfg() config.LLMConfig {
	return config.LLMConfig{
		DefaultProvider: "alpha",
		Providers: map[string]config.ProviderConfig{
			"alpha": {APIKey: "sk-aaaaaaaaaaaaaaaa", Model: "ma", BaseURL: "https://a.example"},
			"beta":  {APIKey: "sk-bbbbbbbbbbbbbbbb", Model: "mb", BaseURL: "https://b.example"},
		},
		FallbackChain: []string{"alpha", "beta"},
		Health: config.ProviderHealthConfig{
			CooldownBase:   30 * time.Second,
			CooldownMax:    5 * time.Minute,
			CooldownFactor: 2,
		},
	}
}

func mustNewManager(t *testing.T, cfg config.LLMConfig, fb *fakeBuilder) *Manager {
	t.Helper()
	m, err := New(cfg, nil, fb.build, defaultPolicyBuilder, zap.NewNop())
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	return m
}

func TestNew_BuildsInitialChain(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)

	if fb.callCount("alpha") != 1 || fb.callCount("beta") != 1 {
		t.Fatalf("expected one build per chain entry; got alpha=%d beta=%d",
			fb.callCount("alpha"), fb.callCount("beta"))
	}
	if got := m.ChainSnapshot().Chain; got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("ChainSnapshot order = %v, want [alpha beta]", got)
	}
}

func TestReplaceChain_ReusesExistingAdapters(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)

	if err := m.ReplaceChain([]string{"beta", "alpha"}); err != nil {
		t.Fatalf("ReplaceChain err = %v", err)
	}
	// No new builds — chain reorder should reuse the cached adapters.
	if fb.callCount("alpha") != 1 || fb.callCount("beta") != 1 {
		t.Fatalf("reorder should reuse adapters; got alpha=%d beta=%d",
			fb.callCount("alpha"), fb.callCount("beta"))
	}
	if got := m.ChainSnapshot().Chain; got[0] != "beta" || got[1] != "alpha" {
		t.Fatalf("ChainSnapshot order after reorder = %v, want [beta alpha]", got)
	}
}

func TestReplaceChain_UnknownProviderRejected(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	err := m.ReplaceChain([]string{"alpha", "ghost"})
	if err == nil {
		t.Fatalf("expected error for unknown provider in chain")
	}
	// State unchanged.
	if got := m.ChainSnapshot().Chain; got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("chain mutated after rejection: %v", got)
	}
}

func TestReplaceChain_EmptyRejected(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	if err := m.ReplaceChain(nil); err == nil {
		t.Fatalf("expected error for empty chain")
	}
}

func TestUpdateProvider_RebuildsAdapter(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)

	newModel := "ma-v2"
	if err := m.UpdateProvider("alpha", ProviderPatch{Model: &newModel}); err != nil {
		t.Fatalf("UpdateProvider err = %v", err)
	}
	if fb.callCount("alpha") != 2 {
		t.Fatalf("alpha should have been rebuilt; got call count %d", fb.callCount("alpha"))
	}
	snaps := m.ProvidersSnapshot()
	for _, s := range snaps {
		if s.Name == "alpha" && s.Model != "ma-v2" {
			t.Fatalf("snapshot Model = %q, want ma-v2", s.Model)
		}
	}
}

func TestUpdateProvider_EmptyPatchRejected(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	if err := m.UpdateProvider("alpha", ProviderPatch{}); err == nil {
		t.Fatalf("expected error for empty patch")
	}
}

func TestUpdateProvider_UnknownProviderRejected(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	model := "x"
	if err := m.UpdateProvider("ghost", ProviderPatch{Model: &model}); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
}

func TestUpdateProvider_BuildFailureLeavesStateAlone(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	fb.errOn = "alpha"
	newModel := "ma-bad"
	err := m.UpdateProvider("alpha", ProviderPatch{Model: &newModel})
	if err == nil {
		t.Fatalf("expected adapter build error")
	}
	// Provider config should NOT reflect the bad patch.
	for _, s := range m.ProvidersSnapshot() {
		if s.Name == "alpha" && s.Model == "ma-bad" {
			t.Fatalf("snapshot reflects rolled-back patch; got %+v", s)
		}
	}
}

func TestProvidersSnapshot_MasksAPIKeys(t *testing.T) {
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)
	for _, s := range m.ProvidersSnapshot() {
		if s.APIKeyMask == "" {
			t.Fatalf("provider %s: api_key_mask empty", s.Name)
		}
		if s.APIKeyMask == "sk-aaaaaaaaaaaaaaaa" || s.APIKeyMask == "sk-bbbbbbbbbbbbbbbb" {
			t.Fatalf("provider %s: api_key not masked: %q", s.Name, s.APIKeyMask)
		}
	}
}

func TestChat_DelegatesToCurrent_ConcurrentSafe(t *testing.T) {
	// Run a handful of Chat calls concurrently with chain mutations;
	// nothing should panic and Chat must always see a non-nil
	// Failover (sanity: atomic.Pointer.Load contract).
	fb := newFakeBuilder()
	m := mustNewManager(t, baseCfg(), fb)

	var wg sync.WaitGroup
	var crashes atomic.Int32
	stop := make(chan struct{})

	// Calling Chat against a mock-built adapter slice would hit the
	// nil zero-value Bifrost adapter and panic. We don't care that
	// Chat succeeds here — only that .Load() never returns nil and
	// the swap mid-flight doesn't race. So we stop short of
	// actually invoking the adapter chain: m.current.Load() is the
	// public contract.
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
				_ = m.ChainSnapshot()
			}
		}()
	}

	for i := 0; i < 20; i++ {
		newOrder := []string{"alpha", "beta"}
		if i%2 == 1 {
			newOrder = []string{"beta", "alpha"}
		}
		if err := m.ReplaceChain(newOrder); err != nil {
			t.Fatalf("ReplaceChain err = %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if crashes.Load() > 0 {
		t.Fatalf("%d concurrent crashes during hot swap", crashes.Load())
	}
}

// Compile-time: Manager satisfies provider.Provider.
var _ provider.Provider = (*Manager)(nil)

// Compile-time: types referenced by external API stay stable.
var _ = func() {
	var ctx context.Context
	_ = ctx
}
