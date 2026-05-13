package registry

import (
	"testing"
)

func newTestManifest() *Manifest {
	return &Manifest{
		Version: 1,
		Providers: map[string]*ProviderSpec{
			"deepseek": {
				DisplayName: "DeepSeek",
				Family:      "deepseek",
				BaseURL:     "https://api.deepseek.com",
				Quirks:      ProviderQuirks{ThinkingParamStyle: "deepseek_type"},
			},
			"openai": {
				DisplayName: "OpenAI",
				Family:      "openai",
				BaseURL:     "https://api.openai.com",
			},
		},
		Models: map[string]*ModelSpec{
			"deepseek/deepseek-v4-flash": {
				Provider: "deepseek", ModelID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash",
				Limits: LimitsSpec{ContextWindow: 1000000},
			},
			"openai/gpt-5.5": {
				Provider: "openai", ModelID: "gpt-5.5", DisplayName: "GPT-5.5",
				Limits: LimitsSpec{ContextWindow: 1050000},
				Supports: SupportsFlags{Vision: true},
			},
		},
	}
}

func TestRegistry_LookupModel(t *testing.T) {
	r := NewRegistry(newTestManifest())
	got := r.LookupModel("openai/gpt-5.5")
	if got == nil || got.DisplayName != "GPT-5.5" {
		t.Errorf("expected GPT-5.5, got %+v", got)
	}
	if r.LookupModel("missing/foo") != nil {
		t.Error("expected nil for unknown model")
	}
}

func TestRegistry_LookupProvider(t *testing.T) {
	r := NewRegistry(newTestManifest())
	if r.LookupProvider("deepseek").Quirks.ThinkingParamStyle != "deepseek_type" {
		t.Error("provider lookup wrong")
	}
	if r.LookupProvider("nope") != nil {
		t.Error("expected nil for unknown provider")
	}
}

func TestRegistry_ListModels_StableOrder(t *testing.T) {
	r := NewRegistry(newTestManifest())
	ids := r.ListModels()
	if len(ids) != 2 {
		t.Fatalf("got %d models, want 2", len(ids))
	}
	if ids[0] >= ids[1] {
		t.Errorf("ListModels not sorted: %v", ids)
	}
}

func TestRegistry_LookupByProviderAndModelID(t *testing.T) {
	r := NewRegistry(newTestManifest())
	got := r.LookupByProviderAndModelID("openai", "gpt-5.5")
	if got == nil || got.DisplayName != "GPT-5.5" {
		t.Errorf("got %+v", got)
	}
	if r.LookupByProviderAndModelID("openai", "nope") != nil {
		t.Error("expected nil for unknown model id")
	}
}
