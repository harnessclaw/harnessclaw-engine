package registry

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestManifest_YAMLRoundTrip(t *testing.T) {
	in := Manifest{
		Version:     1,
		GeneratedAt: time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		Providers: map[string]*ProviderSpec{
			"deepseek": {
				DisplayName: "DeepSeek",
				Family:      "deepseek",
				BaseURL:     "https://api.deepseek.com",
				Protocol:    "openai_compatible",
				Region:      "cn",
				Quirks: ProviderQuirks{
					ThinkingParamStyle:             "deepseek",
					ToolCallsRequireReasoningField: true,
					ExtraParamsPassthroughRequired: true,
					NeedsFirstByteTimeoutMs:        120000,
				},
				Auth: ProviderAuth{
					Type:      "bearer",
					KeyHeader: "Authorization",
					KeyPrefix: "Bearer ",
				},
				Endpoints: ProviderEndpoints{
					ChatCompletions: "/v1/chat/completions",
					ModelsList:      "/v1/models",
				},
			},
		},
		Models: map[string]*ModelSpec{
			"deepseek/deepseek-v4-flash": {
				Provider:    "deepseek",
				ModelID:     "deepseek-v4-flash",
				DisplayName: "DeepSeek V4 Flash",
				Family:      "deepseek-v4",
				Generation:  "v4",
				Modalities:  ModalitySpec{Input: []string{"text"}, Output: []string{"text"}},
				Supports: SupportsFlags{
					Streaming:           true,
					SystemMessages:      true,
					FunctionCalling:     true,
					Reasoning:           true,
					ReasoningCanDisable: true,
					PromptCaching:       true,
				},
				Limits: LimitsSpec{
					ContextWindow:   65536,
					MaxInputTokens:  65535,
					MaxOutputTokens: 8192,
				},
				Defaults: DefaultsSpec{
					Temperature:            0.7,
					TopP:                   1.0,
					MaxOutputTokensDefault: 4096,
				},
			},
		},
	}

	b, err := yaml.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Manifest
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.Version != 1 {
		t.Errorf("version round-trip: got %d", out.Version)
	}
	prov := out.Providers["deepseek"]
	if prov == nil {
		t.Fatal("deepseek provider lost")
	}
	if prov.Quirks.ThinkingParamStyle != "deepseek" {
		t.Errorf("quirks.thinking_param_style: %q", prov.Quirks.ThinkingParamStyle)
	}
	if !prov.Quirks.ToolCallsRequireReasoningField {
		t.Errorf("quirks.tool_calls_require_reasoning_field lost")
	}
	mod := out.Models["deepseek/deepseek-v4-flash"]
	if mod == nil {
		t.Fatal("model lost")
	}
	if mod.Limits.ContextWindow != 65536 {
		t.Errorf("limits.context_window: %d", mod.Limits.ContextWindow)
	}
	if !mod.Supports.Reasoning || !mod.Supports.ReasoningCanDisable {
		t.Errorf("supports.reasoning flags lost: %+v", mod.Supports)
	}
}
