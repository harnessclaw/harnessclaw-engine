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
