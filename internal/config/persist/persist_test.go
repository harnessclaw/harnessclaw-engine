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
  default_provider: "openai"
  # 重要注释：这是 max_retries
  max_retries: 3
  providers:
    # alpha is primary
    alpha:
      base_url: "https://a.example"
      api_key: "sk-alpha-xxxx"
      model: "model-a"
      max_tokens: 4096
    beta:
      base_url: "https://b.example"
      api_key: "sk-beta-xxxx"
      model: "model-b"
      max_tokens: 4096
  fallback_chain:
    - alpha
    - beta
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

	// Comments survive the round-trip.
	for _, want := range []string{
		"# Top-level comment.",
		"# --- LLM ---",
		"# 重要注释：这是 max_retries",
		"# alpha is primary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing comment %q in:\n%s", want, got)
		}
	}
}

func TestSetFallbackChain_ReplacesInPlace(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	if err := f.SetFallbackChain([]string{"beta", "alpha"}); err != nil {
		t.Fatalf("SetFallbackChain: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload via config.Load and verify the chain reflects the new order.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	if got := cfg.LLM.FallbackChain; got[0] != "beta" || got[1] != "alpha" {
		t.Fatalf("fallback_chain = %v, want [beta alpha]", got)
	}

	// Comments still present.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# --- LLM ---") {
		t.Errorf("comment lost after chain rewrite:\n%s", raw)
	}
}

func TestSetProvider_UpdatesExisting(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)

	newCfg := config.ProviderConfig{
		BaseURL:   "https://a2.example",
		APIKey:    "sk-alpha-NEW",
		Model:     "model-a-v2",
		MaxTokens: 8192,
	}
	if err := f.SetProvider("alpha", newCfg); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	alpha := cfg.LLM.Providers["alpha"]
	if alpha.BaseURL != "https://a2.example" || alpha.Model != "model-a-v2" || alpha.APIKey != "sk-alpha-NEW" || alpha.MaxTokens != 8192 {
		t.Fatalf("alpha not updated; got %+v", alpha)
	}
	// Beta untouched.
	beta := cfg.LLM.Providers["beta"]
	if beta.BaseURL != "https://b.example" || beta.APIKey != "sk-beta-xxxx" {
		t.Fatalf("beta clobbered; got %+v", beta)
	}
}

func TestSetProvider_InsertsNew(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	gamma := config.ProviderConfig{
		BaseURL:   "https://g.example",
		APIKey:    "sk-gamma",
		Model:     "model-g",
		MaxTokens: 2048,
	}
	if err := f.SetProvider("gamma", gamma); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load reread: %v", err)
	}
	g, ok := cfg.LLM.Providers["gamma"]
	if !ok {
		t.Fatalf("gamma not inserted; providers = %v", cfg.LLM.Providers)
	}
	if g.Model != "model-g" {
		t.Fatalf("gamma fields wrong; got %+v", g)
	}
}

func TestSetFallbackChain_EmptyRemovesKey(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	f, _ := Load(path)
	if err := f.SetFallbackChain(nil); err != nil {
		t.Fatalf("SetFallbackChain nil: %v", err)
	}
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "fallback_chain") {
		t.Fatalf("fallback_chain should be removed:\n%s", raw)
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
	// Make the directory unwritable to force rename failure.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	original := sampleYAML
	_ = os.WriteFile(path, []byte(original), 0644)
	f, _ := Load(path)
	_ = f.SetFallbackChain([]string{"beta"})
	// Strip write perms on the dir to make CreateTemp fail.
	_ = os.Chmod(dir, 0500)
	defer os.Chmod(dir, 0700)

	if err := f.Save(); err == nil {
		t.Skip("filesystem allowed write despite chmod 500 (uid 0?)")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Fatalf("original file mutated after failed save")
	}
}
