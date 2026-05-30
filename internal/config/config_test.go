package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointConfig_ModelTypeYAMLRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "cfg.yaml")
	raw := `
llm:
  providers:
    anthropic:
      type: anthropic
      api_key: dummy
      endpoints:
        claude-opus-4-7:
          model: claude-opus-4-7
          model_type: [vision, pdf, reasoning, tools]
`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ep := cfg.LLM.Providers["anthropic"].Endpoints["claude-opus-4-7"]
	want := []string{"vision", "pdf", "reasoning", "tools"}
	if len(ep.ModelType) != len(want) {
		t.Fatalf("model_type len: got %v want %v", ep.ModelType, want)
	}
	for i, tok := range want {
		if ep.ModelType[i] != tok {
			t.Errorf("[%d]: got %q want %q", i, ep.ModelType[i], tok)
		}
	}
}

func TestEndpointConfig_ModelTypeAbsentIsNil(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
llm:
  providers:
    anthropic:
      type: anthropic
      api_key: dummy
      endpoints:
        claude-opus-4-7:
          model: claude-opus-4-7
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ep := cfg.LLM.Providers["anthropic"].Endpoints["claude-opus-4-7"]
	if len(ep.ModelType) != 0 {
		t.Errorf("absent model_type should be nil/empty, got %v", ep.ModelType)
	}
}

func TestLoad_EndpointGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
llm:
  providers:
    openai:
      type: openai
      base_url: https://api.openai.com
      api_key: sk-test
      endpoints:
        gpt-5:
          model: gpt-5
          group: "GPT-5"
        gpt-3:
          model: gpt-3.5-turbo
`
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.LLM.Providers["openai"]
	if got := p.Endpoints["gpt-5"].Group; got != "GPT-5" {
		t.Errorf("gpt-5 group = %q, want GPT-5", got)
	}
	if got := p.Endpoints["gpt-3"].Group; got != "" {
		t.Errorf("gpt-3 group = %q, want \"\" (omitted)", got)
	}
}
