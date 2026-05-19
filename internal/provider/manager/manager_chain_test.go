package manager

import (
	"testing"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/provider/registry"
)

// TestChainSupports_IntersectionAcrossModels exercises the
// fallback-chain capability gate: when primary supports a feature
// but a fallback in the chain doesn't, the intersected SupportsFlags
// must drop the feature. Otherwise a fail-over hop after primary
// throws would send image content to a text-only model and surface
// as an opaque 400 from the upstream provider.
func TestChainSupports_IntersectionAcrossModels(t *testing.T) {
	m := &Manager{
		agent: config.AgentConfig{
			Primary:       "anthropic:claude-opus-4-7",
			FallbackChain: []string{"anthropic:claude-haiku-4-5"},
		},
	}
	supports := func(key string) registry.SupportsFlags {
		switch key {
		case "anthropic:claude-opus-4-7":
			return registry.SupportsFlags{Vision: true, FunctionCalling: true, Reasoning: true}
		case "anthropic:claude-haiku-4-5":
			return registry.SupportsFlags{FunctionCalling: true} // no Vision, no Reasoning
		}
		return registry.SupportsFlags{}
	}
	got := m.ChainSupports(supports)
	if got.Vision {
		t.Error("intersection must clear Vision when any chain entry lacks it")
	}
	if got.Reasoning {
		t.Error("intersection must clear Reasoning when any chain entry lacks it")
	}
	if !got.FunctionCalling {
		t.Error("FunctionCalling should remain since both chain entries support it")
	}
}

// TestChainSupports_PrimaryOnly is the common case: no fallback
// configured, intersection collapses to primary's supports.
func TestChainSupports_PrimaryOnly(t *testing.T) {
	m := &Manager{agent: config.AgentConfig{Primary: "anthropic:claude-opus-4-7"}}
	supports := func(key string) registry.SupportsFlags {
		if key == "anthropic:claude-opus-4-7" {
			return registry.SupportsFlags{Vision: true, PDFInput: true}
		}
		return registry.SupportsFlags{}
	}
	got := m.ChainSupports(supports)
	if !got.Vision || !got.PDFInput {
		t.Errorf("primary supports lost: %+v", got)
	}
}

// TestChainSupports_EmptyPrimary returns an all-false SupportsFlags
// (fail-closed) when no primary is configured. Router treats this as
// "no model can handle anything" — the gate rejects all multimodal
// content until the operator configures a primary endpoint.
func TestChainSupports_EmptyPrimary(t *testing.T) {
	m := &Manager{}
	supports := func(_ string) registry.SupportsFlags {
		return registry.SupportsFlags{Vision: true}
	}
	got := m.ChainSupports(supports)
	if got.Vision {
		t.Error("no-primary intersection must be all-false")
	}
}

// TestChainSupports_UnknownEntryDropsAllSupports verifies that a
// chain entry not found in the registry (lookup returns zero
// SupportsFlags) drags the intersection to false for every flag —
// this is the safe default for unmapped models.
func TestChainSupports_UnknownEntryDropsAllSupports(t *testing.T) {
	m := &Manager{
		agent: config.AgentConfig{
			Primary:       "anthropic:claude-opus-4-7",
			FallbackChain: []string{"deepseek:unknown-model"},
		},
	}
	supports := func(key string) registry.SupportsFlags {
		if key == "anthropic:claude-opus-4-7" {
			return registry.SupportsFlags{Vision: true, FunctionCalling: true}
		}
		return registry.SupportsFlags{} // unknown
	}
	got := m.ChainSupports(supports)
	if got.Vision || got.FunctionCalling {
		t.Errorf("unknown chain entry must drop all flags to false: %+v", got)
	}
}
