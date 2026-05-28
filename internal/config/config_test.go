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

func TestBrowserAgentConfig_DefaultsDisabled(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Tools.BrowserAgent.Enabled {
		t.Fatal("browser agent should default to disabled")
	}
	if cfg.Tools.BrowserAgent.MaxSteps != 30 {
		t.Errorf("max_steps = %d, want 30", cfg.Tools.BrowserAgent.MaxSteps)
	}
	if cfg.Tools.BrowserAgent.DefaultVisibility != "visible" {
		t.Errorf("default_visibility = %q, want visible", cfg.Tools.BrowserAgent.DefaultVisibility)
	}
	if cfg.Tools.BrowserAgent.PreferredSearchEngine != "duckduckgo" {
		t.Errorf("preferred_search_engine = %q, want duckduckgo", cfg.Tools.BrowserAgent.PreferredSearchEngine)
	}
	if cfg.Tools.BrowserAgent.HumanTakeoverTimeout.String() != "2m0s" {
		t.Errorf("human_takeover_timeout = %s, want 2m0s", cfg.Tools.BrowserAgent.HumanTakeoverTimeout)
	}
	if cfg.Tools.BrowserAgent.CLITimeout.String() != "25s" {
		t.Errorf("cli_timeout = %s, want 25s", cfg.Tools.BrowserAgent.CLITimeout)
	}
}

func TestBrowserAgentConfig_YAMLRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
tools:
  browser_agent:
    enabled: true
    binary_path: "/opt/harnessclaw/agent-browser"
    default_visibility: "visible"
    max_steps: 12
    preferred_search_engine: "google"
    blocked_domains: ["blocked.example"]
    human_takeover_timeout: "45s"
    session_persistence: false
    cli_timeout: "9s"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := cfg.Tools.BrowserAgent
	if !got.Enabled {
		t.Fatal("browser_agent.enabled should load true")
	}
	if got.BinaryPath != "/opt/harnessclaw/agent-browser" {
		t.Errorf("binary_path = %q", got.BinaryPath)
	}
	if got.DefaultVisibility != "visible" {
		t.Errorf("default_visibility = %q", got.DefaultVisibility)
	}
	if got.MaxSteps != 12 {
		t.Errorf("max_steps = %d", got.MaxSteps)
	}
	if got.PreferredSearchEngine != "google" {
		t.Errorf("preferred_search_engine = %q", got.PreferredSearchEngine)
	}
	if len(got.BlockedDomains) != 1 || got.BlockedDomains[0] != "blocked.example" {
		t.Errorf("blocked_domains = %v", got.BlockedDomains)
	}
	if got.HumanTakeoverTimeout.String() != "45s" {
		t.Errorf("human_takeover_timeout = %s", got.HumanTakeoverTimeout)
	}
	if got.SessionPersistence {
		t.Error("session_persistence should load false")
	}
	if got.CLITimeout.String() != "9s" {
		t.Errorf("cli_timeout = %s", got.CLITimeout)
	}
}
