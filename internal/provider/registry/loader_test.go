package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestFromYAML_HappyPath(t *testing.T) {
	yamlSrc := []byte(`
version: 1
generated_at: 2026-05-13T12:00:00Z
providers:
  deepseek:
    display_name: DeepSeek
    family: deepseek
    base_url: https://api.deepseek.com
    protocol: openai_compatible
    region: cn
    quirks:
      thinking_param_style: deepseek_type
      tool_calls_require_reasoning_field: true
      extra_params_passthrough_required: true
    auth:
      type: bearer
      key_header: Authorization
      key_prefix: "Bearer "
    endpoints:
      chat_completions: /v1/chat/completions
      models_list: /v1/models
models:
  "deepseek/deepseek-v4-flash":
    provider: deepseek
    model_id: deepseek-v4-flash
    display_name: "DeepSeek V4 Flash"
    family: deepseek-v4
    modalities: { input: [text], output: [text] }
    supports:
      streaming: true
      function_calling: true
      reasoning: true
      reasoning_can_disable: true
    limits:
      context_window: 65536
      max_input_tokens: 65535
      max_output_tokens: 8192
    defaults:
      temperature: 0.7
      top_p: 1.0
      max_output_tokens_default: 4096
`)
	m, err := LoadManifestFromYAML(yamlSrc)
	if err != nil {
		t.Fatalf("LoadManifestFromYAML: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
	if got := m.Providers["deepseek"]; got == nil || got.Quirks.ThinkingParamStyle != "deepseek_type" {
		t.Errorf("deepseek provider wrong: %+v", got)
	}
	if got := m.Models["deepseek/deepseek-v4-flash"]; got == nil || got.Limits.ContextWindow != 65536 {
		t.Errorf("model wrong: %+v", got)
	}
}

func TestLoadManifestFromYAML_RejectsModelWithUnknownProvider(t *testing.T) {
	yamlSrc := []byte(`
version: 1
providers: {}
models:
  "ghost/foo":
    provider: ghost
    model_id: foo
    modalities: { input: [text], output: [text] }
    supports: {}
    limits: { context_window: 1024, max_input_tokens: 1024, max_output_tokens: 256 }
    defaults: { temperature: 0.7, top_p: 1.0, max_output_tokens_default: 128 }
`)
	if _, err := LoadManifestFromYAML(yamlSrc); err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention 'ghost': %v", err)
	}
}

func TestLoadManifestFromYAML_RejectsMismatchedProviderField(t *testing.T) {
	yamlSrc := []byte(`
version: 1
providers:
  deepseek: { display_name: DeepSeek, family: deepseek, base_url: x, protocol: openai_compatible, region: cn, quirks: {}, auth: {type: bearer}, endpoints: {chat_completions: /, models_list: /} }
models:
  "deepseek/x":
    provider: openai
    model_id: x
    modalities: { input: [text], output: [text] }
    supports: {}
    limits: { context_window: 1, max_input_tokens: 1, max_output_tokens: 1 }
    defaults: { temperature: 0, top_p: 0, max_output_tokens_default: 0 }
`)
	if _, err := LoadManifestFromYAML(yamlSrc); err == nil {
		t.Fatal("expected error: model key prefix and provider field disagree")
	}
}

func TestLoadManifestFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nproviders: {}\nmodels: {}"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m, err := LoadManifestFromFile(path)
	if err != nil {
		t.Fatalf("LoadManifestFromFile: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("version = %d", m.Version)
	}
}
