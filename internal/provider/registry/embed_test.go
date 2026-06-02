package registry

import "testing"

func TestDefaultManifest_Loads(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	wantProviders := []string{"openai", "gpt-image", "anthropic", "google", "deepseek", "zhipu", "moonshot", "minimax", "doubao"}
	for _, p := range wantProviders {
		if m.Providers[p] == nil {
			t.Errorf("provider %q missing", p)
		}
	}
	if len(m.Models) < 15 {
		t.Errorf("expected ≥15 models, got %d", len(m.Models))
	}
}

func TestDefaultManifest_ImageGenerationModels(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	for _, key := range []string{
		"gpt-image/gpt-image-2",
		"doubao/doubao-seedream-5-0-260128",
		"doubao/doubao-seedream-5-0-lite-260128",
		"doubao/doubao-seedream-4-5-251128",
		"doubao/doubao-seedream-4-0-250828",
	} {
		mod := m.Models[key]
		if mod == nil {
			t.Fatalf("model %q missing", key)
		}
		if !mod.Supports.ImageGeneration {
			t.Errorf("model %q must declare supports.image_generation", key)
		}
		if !containsString(mod.Modalities.Output, "image") {
			t.Errorf("model %q must declare image output, got %v", key, mod.Modalities.Output)
		}
	}
}

func TestDefaultManifest_DeepSeekQuirks(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	p := m.Providers["deepseek"]
	if !p.Quirks.ToolCallsRequireReasoningField {
		t.Error("DeepSeek tool_calls_require_reasoning_field must be true")
	}
	if !p.Quirks.ExtraParamsPassthroughRequired {
		t.Error("DeepSeek extra_params_passthrough_required must be true")
	}
	if p.Quirks.ThinkingParamStyle != "deepseek_type" {
		t.Errorf("DeepSeek thinking_param_style = %q, want deepseek_type", p.Quirks.ThinkingParamStyle)
	}
}

func TestDefaultManifest_AnthropicExplicitCacheControl(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	if !m.Providers["anthropic"].Quirks.ExplicitCacheControl {
		t.Error("Anthropic explicit_cache_control must be true")
	}
}

func TestDefaultManifest_VisionFlagsConsistent(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	for key, mod := range m.Models {
		hasImageInput := false
		for _, mod := range mod.Modalities.Input {
			if mod == "image" {
				hasImageInput = true
				break
			}
		}
		if hasImageInput != mod.Supports.Vision {
			t.Errorf("model %q: modalities.input contains 'image' (%v) but supports.vision=%v — must agree",
				key, hasImageInput, mod.Supports.Vision)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
