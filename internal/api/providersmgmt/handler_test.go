package providersmgmt

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/provider/failover"
	"harnessclaw-go/internal/provider/manager"
)

const sampleYAML = `# Top-level note
server:
  host: "0.0.0.0"
  port: 8080

llm:
  default_provider: "alpha"
  max_retries: 3
  providers:
    # alpha is primary
    alpha:
      type: anthropic
      base_url: "https://a.example"
      api_key: "sk-alpha-old-key-xxxx"
      model: "model-a"
      max_tokens: 4096
    beta:
      type: openai
      base_url: "https://b.example"
      api_key: "sk-beta-old-key-xxxx"
      model: "model-b"
      max_tokens: 4096
  fallback_chain:
    - alpha
    - beta
  health:
    cooldown_base: "30s"
    cooldown_max: "5m"
    cooldown_factor: 2
`

func setupTest(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(sampleYAML), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	build := func(_ string, _ config.ProviderConfig, _ bool) (*bifrost.Adapter, error) {
		return &bifrost.Adapter{}, nil
	}
	policy := func(_ config.ProviderHealthConfig) (failover.RetryPolicy, failover.RetryPolicy, failover.RetryPolicy) {
		return failover.FastPolicy, failover.MediumPolicy, failover.ProbePolicy
	}
	mgr, err := manager.New(cfg.LLM, nil, build, policy, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr, cfgPath, zap.NewNop()), cfgPath
}

func TestGet_Providers_ReturnsAPIKeyVerbatim(t *testing.T) {
	h, _ := setupTest(t)

	req := httptest.NewRequest("GET", "/api/v1/providers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// API is intentionally not redacted — clients pre-fill PATCH
	// forms with the existing key.
	if !strings.Contains(body, "sk-alpha-old-key-xxxx") {
		t.Fatalf("response missing raw api_key:\n%s", body)
	}
	if !strings.Contains(body, `"api_key"`) {
		t.Fatalf("response missing api_key field:\n%s", body)
	}
	if strings.Contains(body, "api_key_mask") {
		t.Fatalf("response still emits api_key_mask field (should be removed):\n%s", body)
	}
}

func TestGet_Chain_ReturnsCurrentOrderAndHealth(t *testing.T) {
	h, _ := setupTest(t)

	req := httptest.NewRequest("GET", "/api/v1/providers/fallback-chain", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Data struct {
			Chain   []string                  `json:"chain"`
			Entries []failover.ProviderHealth `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resp.Data.Chain; got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("chain = %v, want [alpha beta]", got)
	}
	if len(resp.Data.Entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(resp.Data.Entries))
	}
	for _, e := range resp.Data.Entries {
		if e.State != "healthy" {
			t.Errorf("entry %s state = %s, want healthy", e.Name, e.State)
		}
	}
}

func TestPut_Chain_ReordersAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)

	body, _ := json.Marshal(map[string]any{"chain": []string{"beta", "alpha"}})
	req := httptest.NewRequest("PUT", "/api/v1/providers/fallback-chain", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// Reload from disk — yaml must reflect the new order.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.LLM.FallbackChain; got[0] != "beta" || got[1] != "alpha" {
		t.Fatalf("yaml chain = %v, want [beta alpha]", got)
	}
	// Comments survived.
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "# alpha is primary") {
		t.Fatalf("yaml comment lost:\n%s", string(raw))
	}
}

func TestPut_Chain_RejectsUnknownProvider(t *testing.T) {
	h, cfgPath := setupTest(t)
	original, _ := os.ReadFile(cfgPath)

	body, _ := json.Marshal(map[string]any{"chain": []string{"alpha", "ghost"}})
	req := httptest.NewRequest("PUT", "/api/v1/providers/fallback-chain", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// yaml unchanged.
	after, _ := os.ReadFile(cfgPath)
	if string(original) != string(after) {
		t.Fatalf("yaml mutated despite rejected request")
	}
}

func TestPut_Chain_EmptyRejected(t *testing.T) {
	h, _ := setupTest(t)
	body, _ := json.Marshal(map[string]any{"chain": []string{}})
	req := httptest.NewRequest("PUT", "/api/v1/providers/fallback-chain", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPatch_Provider_UpdatesAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)

	body, _ := json.Marshal(map[string]any{
		"model":   "model-a-v2",
		"api_key": "sk-alpha-NEW",
	})
	req := httptest.NewRequest("PATCH", "/api/v1/providers/alpha", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.LLM.Providers["alpha"]
	if a.Model != "model-a-v2" || a.APIKey != "sk-alpha-NEW" {
		t.Fatalf("yaml not updated: %+v", a)
	}
	// Beta untouched.
	b := cfg.LLM.Providers["beta"]
	if b.APIKey != "sk-beta-old-key-xxxx" {
		t.Fatalf("beta clobbered: %+v", b)
	}
}

func TestPatch_Provider_TypeSwitch(t *testing.T) {
	h, cfgPath := setupTest(t)
	body, _ := json.Marshal(map[string]any{"type": "openai"})
	req := httptest.NewRequest("PATCH", "/api/v1/providers/alpha", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Providers["alpha"].Type != "openai" {
		t.Fatalf("alpha type after patch = %q, want openai", cfg.LLM.Providers["alpha"].Type)
	}
}

func TestPatch_Provider_UnknownTypeRejected(t *testing.T) {
	h, _ := setupTest(t)
	body, _ := json.Marshal(map[string]any{"type": "kimi"})
	req := httptest.NewRequest("PATCH", "/api/v1/providers/alpha", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown type", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("response should mention 'not allowed'; got %s", rec.Body.String())
	}
}

func TestPatch_Provider_EmptyPatchRejected(t *testing.T) {
	h, _ := setupTest(t)
	req := httptest.NewRequest("PATCH", "/api/v1/providers/alpha", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPatch_Provider_UnknownNameRejected(t *testing.T) {
	h, _ := setupTest(t)
	body, _ := json.Marshal(map[string]any{"model": "x"})
	req := httptest.NewRequest("PATCH", "/api/v1/providers/ghost", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRoute_MethodNotAllowed(t *testing.T) {
	h, _ := setupTest(t)
	req := httptest.NewRequest("DELETE", "/api/v1/providers/alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestRoute_UnknownPath(t *testing.T) {
	h, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/api/v1/providers/alpha/extra", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-OK for unknown nested path; body=%s", rec.Body.String())
	}
}

// Verify timezone-independent comparison won't break health entries.
func TestChainSnapshot_TimestampsAreRFC3339(t *testing.T) {
	h, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/api/v1/providers/fallback-chain", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp struct {
		Data struct {
			Entries []failover.ProviderHealth `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, e := range resp.Data.Entries {
		if e.TrippedUntil == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, e.TrippedUntil); err != nil {
			t.Errorf("entry %s: tripped_until %q not RFC3339: %v", e.Name, e.TrippedUntil, err)
		}
	}
}
