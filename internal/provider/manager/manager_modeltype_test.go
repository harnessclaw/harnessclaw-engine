package manager

import (
	"testing"

	"harnessclaw-go/internal/config"
)

func TestLookupEndpointModelType_ReturnsConfigured(t *testing.T) {
	m := &Manager{cfg: config.LLMConfig{Providers: map[string]config.ProviderConfig{
		"anthropic": {Endpoints: map[string]config.EndpointConfig{
			"claude-opus-4-7": {ModelType: []string{"vision", "tools"}},
		}},
	}}}
	got, ok := m.LookupEndpointModelType("anthropic:claude-opus-4-7")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(got) != 2 || got[0] != "vision" || got[1] != "tools" {
		t.Errorf("got %v", got)
	}
}

func TestLookupEndpointModelType_EmptyReturnsFalse(t *testing.T) {
	m := &Manager{cfg: config.LLMConfig{Providers: map[string]config.ProviderConfig{
		"anthropic": {Endpoints: map[string]config.EndpointConfig{
			"claude-opus-4-7": {}, // ModelType nil
		}},
	}}}
	_, ok := m.LookupEndpointModelType("anthropic:claude-opus-4-7")
	if ok {
		t.Error("empty model_type should report not-configured (fallback semantic)")
	}
}

func TestLookupEndpointModelType_UnknownEndpoint(t *testing.T) {
	m := &Manager{cfg: config.LLMConfig{}}
	_, ok := m.LookupEndpointModelType("ghost:x")
	if ok {
		t.Error("unknown endpoint must return ok=false")
	}
}

func TestLookupEndpointModelType_DefensiveCopy(t *testing.T) {
	m := &Manager{cfg: config.LLMConfig{Providers: map[string]config.ProviderConfig{
		"anthropic": {Endpoints: map[string]config.EndpointConfig{
			"x": {ModelType: []string{"vision"}},
		}},
	}}}
	got, _ := m.LookupEndpointModelType("anthropic:x")
	got[0] = "mutated"
	again, _ := m.LookupEndpointModelType("anthropic:x")
	if again[0] != "vision" {
		t.Error("caller mutation leaked into manager state")
	}
}
