package persist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
)

const sampleYAML = `# Top-level comment.
server:
  host: "0.0.0.0"
  port: 8080

# --- LLM ---
llm:
  max_retries: 3
  # 重要注释：这是 default_max_tokens
  default_max_tokens: 8192
  providers:
    # alpha is primary
    alpha:
      type: anthropic
      base_url: "https://a.example"
      api_key: "sk-alpha-xxxx"
      endpoints:
        claude-46:
          model: claude-sonnet-4-6
          max_tokens: 16384
    beta:
      type: openai
      base_url: "https://b.example"
      api_key: "sk-beta-xxxx"
      endpoints:
        gpt-5:
          model: gpt-5-turbo
          max_tokens: 4096

# --- Agent ---
agent:
  primary: "alpha:claude-46"
  fallback_chain:
    - "beta:gpt-5"
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoad_RejectsMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/path/x.yaml"); err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestSaveRoundTrip_PreservesComments(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	for _, want := range []string{
		"# Top-level comment.",
		"# --- LLM ---",
		"# 重要注释：这是 default_max_tokens",
		"# alpha is primary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing comment %q in:\n%s", want, got)
		}
	}
}

func TestSetAgent_ReplacesInPlace(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	if err := f.SetAgent(config.AgentConfig{
		Primary:       "beta:gpt-5",
		FallbackChain: []string{"alpha:claude-46"},
	}); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	if cfg.Agent.Primary != "beta:gpt-5" {
		t.Fatalf("agent.primary = %q, want beta:gpt-5", cfg.Agent.Primary)
	}
	if got := cfg.Agent.FallbackChain; len(got) != 1 || got[0] != "alpha:claude-46" {
		t.Fatalf("agent.fallback_chain = %v, want [alpha:claude-46]", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# --- LLM ---") {
		t.Errorf("LLM comment lost after agent rewrite:\n%s", raw)
	}
}

func TestSetProviderCreds_UpdatesExisting(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)

	newCreds := config.ProviderConfig{
		Type:    "anthropic",
		BaseURL: "https://a2.example",
		APIKey:  "sk-alpha-NEW",
	}
	if err := f.SetProviderCreds("alpha", newCreds); err != nil {
		t.Fatalf("SetProviderCreds: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	alpha := cfg.LLM.Providers["alpha"]
	if alpha.BaseURL != "https://a2.example" || alpha.APIKey != "sk-alpha-NEW" || alpha.Type != "anthropic" {
		t.Fatalf("alpha creds not updated: %+v", alpha)
	}
	// Endpoints survived the creds rewrite.
	if _, ok := alpha.Endpoints["claude-46"]; !ok {
		t.Fatalf("endpoints lost during SetProviderCreds: %+v", alpha.Endpoints)
	}
	// Beta untouched.
	beta := cfg.LLM.Providers["beta"]
	if beta.BaseURL != "https://b.example" {
		t.Fatalf("beta clobbered: %+v", beta)
	}
}

func TestSetEndpoint_ModelTypeRoundTrip(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := f.SetEndpoint("alpha", "claude-46", config.EndpointConfig{
		Model:     "claude-sonnet-4-6",
		ModelType: []string{"vision", "tools"},
	}); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "model_type") {
		t.Errorf("model_type not persisted, yaml:\n%s", raw)
	}
	if !strings.Contains(string(raw), "vision") || !strings.Contains(string(raw), "tools") {
		t.Errorf("tokens missing, yaml:\n%s", raw)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]
	want := []string{"vision", "tools"}
	if len(ep.ModelType) != len(want) {
		t.Fatalf("model_type: got %v want %v", ep.ModelType, want)
	}
	for i, tok := range want {
		if ep.ModelType[i] != tok {
			t.Errorf("[%d]: got %q want %q", i, ep.ModelType[i], tok)
		}
	}
}

func TestSetEndpoint_AddsAndUpdates(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)

	// Add a new endpoint under alpha.
	if err := f.SetEndpoint("alpha", "claude-45", config.EndpointConfig{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 16384,
	}); err != nil {
		t.Fatalf("SetEndpoint new: %v", err)
	}
	// Update existing endpoint claude-46.
	if err := f.SetEndpoint("alpha", "claude-46", config.EndpointConfig{
		Model:     "claude-sonnet-4-6-v2",
		MaxTokens: 32768,
	}); err != nil {
		t.Fatalf("SetEndpoint update: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	alpha := cfg.LLM.Providers["alpha"]
	if alpha.Endpoints["claude-45"].Model != "claude-sonnet-4-5" {
		t.Fatalf("claude-45 not added: %+v", alpha.Endpoints)
	}
	if alpha.Endpoints["claude-46"].Model != "claude-sonnet-4-6-v2" || alpha.Endpoints["claude-46"].MaxTokens != 32768 {
		t.Fatalf("claude-46 not updated: %+v", alpha.Endpoints["claude-46"])
	}
}

// TestSaveForcesBlockStyle reproduces the "flow style endpoints
// compacted into one line" bug: yaml file initially has
// `endpoints: {}` (flow). Adding a new endpoint via SetEndpoint
// and Save should produce BLOCK style output, not preserve flow.
func TestSaveForcesBlockStyle(t *testing.T) {
	yaml := `llm:
  providers:
    openai:
      type: openai
      base_url: "x"
      api_key: "k"
      endpoints: {}
`
	path := writeTemp(t, yaml)
	f, _ := Load(path)
	if err := f.SetEndpoint("openai", "gpt-5", config.EndpointConfig{
		Model:     "gpt-5",
		MaxTokens: 4096,
	}); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	if err := f.SetEndpoint("openai", "gpt-4", config.EndpointConfig{
		Model:     "gpt-4",
		MaxTokens: 4096,
	}); err != nil {
		t.Fatalf("SetEndpoint: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	if strings.Contains(got, "{model:") || strings.Contains(got, "endpoints: {") {
		t.Fatalf("output should be block style, not flow:\n%s", got)
	}
	// Each endpoint key should be on its own line.
	if !strings.Contains(got, "gpt-5:") || !strings.Contains(got, "gpt-4:") {
		t.Fatalf("expected each endpoint on its own line:\n%s", got)
	}
}

// TestSetEndpoint_RecoversFromNullEndpoints reproduces the
// "endpoints is not a mapping" 500 error: yaml file has
// `endpoints:` followed by no value (null scalar). SetEndpoint
// should rebuild the mapping in place rather than fail.
func TestSetEndpoint_RecoversFromNullEndpoints(t *testing.T) {
	yaml := `llm:
  providers:
    openai:
      endpoints:
      type: openai
      base_url: "https://api.openai.com"
      api_key: "sk-x"
`
	path := writeTemp(t, yaml)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := f.SetEndpoint("openai", "gpt-5", config.EndpointConfig{
		Model:     "gpt-5",
		MaxTokens: 4096,
	}); err != nil {
		t.Fatalf("SetEndpoint with null endpoints: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := cfg.LLM.Providers["openai"].Endpoints["gpt-5"]
	if got.Model != "gpt-5" || got.MaxTokens != 4096 {
		t.Fatalf("endpoint not written: %+v", got)
	}
}

// TestRemoveEndpoint_DropsEmptyEndpointsKey confirms that deleting
// the last endpoint removes the `endpoints:` parent key entirely,
// so we don't leave a null mapping behind.
func TestRemoveEndpoint_DropsEmptyEndpointsKey(t *testing.T) {
	yaml := `llm:
  providers:
    openai:
      type: openai
      base_url: "x"
      api_key: "k"
      endpoints:
        gpt-5:
          model: gpt-5
`
	path := writeTemp(t, yaml)
	f, _ := Load(path)
	if err := f.RemoveEndpoint("openai", "gpt-5"); err != nil {
		t.Fatalf("RemoveEndpoint: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "endpoints:") {
		t.Fatalf("endpoints: key should be gone after removing last entry:\n%s", string(raw))
	}
}

func TestRemoveEndpoint(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	if err := f.RemoveEndpoint("alpha", "claude-46"); err != nil {
		t.Fatalf("RemoveEndpoint: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cfg, _ := config.Load(path)
	if _, ok := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]; ok {
		t.Fatalf("claude-46 still present after remove")
	}
}

func TestSetAgent_EmptyChainRemovesKey(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	if err := f.SetAgent(config.AgentConfig{Primary: "alpha:claude-46"}); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "fallback_chain") {
		t.Fatalf("fallback_chain should be removed when empty:\n%s", raw)
	}
}

func TestSetAgent_MigratesLLMFallbackChain(t *testing.T) {
	// Old config layout has llm.fallback_chain; SetAgent should
	// strip it on the same save so the on-disk file lands cleanly
	// in the new shape.
	oldLayout := `llm:
  providers:
    alpha:
      type: anthropic
      api_key: x
      endpoints:
        claude-46:
          model: claude-sonnet-4-6
  fallback_chain:
    - "alpha:claude-46"
`
	path := writeTemp(t, oldLayout)
	f, _ := Load(path)
	if err := f.SetAgent(config.AgentConfig{
		Primary: "alpha:claude-46",
	}); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "fallback_chain") {
		t.Fatalf("legacy llm.fallback_chain should be migrated away:\n%s", raw)
	}
	if !strings.Contains(string(raw), "agent:") {
		t.Fatalf("agent: block missing:\n%s", raw)
	}
}

func TestLoad_RejectsInvalidYAML(t *testing.T) {
	path := writeTemp(t, "this is: not: valid: yaml: at: all")
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestAtomicSave_FailureLeavesOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	original := sampleYAML
	_ = os.WriteFile(path, []byte(original), 0644)
	f, _ := Load(path)
	_ = f.SetAgent(config.AgentConfig{Primary: "beta:gpt-5"})
	_ = os.Chmod(dir, 0500)
	defer os.Chmod(dir, 0700)
	if err := f.Save(); err == nil {
		t.Skip("filesystem allowed write despite chmod 500")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Fatalf("original file mutated after failed save")
	}
}

func TestSetToolConfig_WebSearch_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte(`tools:
  web_search:
    enabled: false
    api_key: "old"
    api_secret: "old"
    app_id: "old"
    limit: 5
`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]any{
		"enabled":    true,
		"api_key":    "new-key",
		"api_secret": "new-secret",
		"app_id":     "new-app",
		"host":       "search-api.example.com",
		"path":       "/biz/search",
		"limit":      10,
	}
	if err := f.SetToolConfig("web_search", want); err != nil {
		t.Fatalf("SetToolConfig: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	for _, fragment := range []string{
		"enabled: true",
		`api_key: "new-key"`,
		`api_secret: "new-secret"`,
		`app_id: "new-app"`,
		`host: "search-api.example.com"`,
		`path: "/biz/search"`,
		"limit: 10",
	} {
		if !strings.Contains(string(got), fragment) {
			t.Errorf("expected fragment %q in written file, got:\n%s", fragment, got)
		}
	}
}

func TestSetToolConfig_CreatesMissingToolsBlock(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte("agent:\n  primary: foo:bar\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := f.SetToolConfig("tavily_search", map[string]any{
		"enabled":     true,
		"api_key":     "tvly-xyz",
		"max_results": 5,
	}); err != nil {
		t.Fatalf("SetToolConfig: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _ := os.ReadFile(path)
	for _, fragment := range []string{
		"tools:",
		"tavily_search:",
		"enabled: true",
		`api_key: "tvly-xyz"`,
		"max_results: 5",
	} {
		if !strings.Contains(string(got), fragment) {
			t.Errorf("expected fragment %q, got:\n%s", fragment, got)
		}
	}
}

func TestSetToolConfig_RejectsEmptyName(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte("tools: {}\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := f.SetToolConfig("", map[string]any{"enabled": true}); err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
}

func TestSetEndpoint_GroupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := `
llm:
  providers:
    openai:
      type: openai
      base_url: https://api.openai.com
      api_key: sk-x
      endpoints:
        gpt-5:
          model: gpt-5
`
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetEndpoint("openai", "gpt-5", config.EndpointConfig{
		Model: "gpt-5",
		Group: "GPT-5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "group: GPT-5") {
		t.Errorf("expected `group: GPT-5` in yaml, got:\n%s", out)
	}

	// Clear via SetEndpoint with Group=""; expect the key to vanish.
	if err := f.SetEndpoint("openai", "gpt-5", config.EndpointConfig{
		Model: "gpt-5",
		Group: "",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	out, _ = os.ReadFile(path)
	if strings.Contains(string(out), "group:") {
		t.Errorf("expected `group:` key removed when Group=\"\", got:\n%s", out)
	}
}
