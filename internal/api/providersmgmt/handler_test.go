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
  max_retries: 3
  default_max_tokens: 8192
  providers:
    # alpha is primary
    alpha:
      type: anthropic
      base_url: "https://a.example"
      api_key: "sk-alpha-old-key-xxxx"
      endpoints:
        claude-46:
          model: claude-sonnet-4-6
          max_tokens: 16384
        claude-45:
          model: claude-sonnet-4-5
          max_tokens: 16384
    beta:
      type: openai
      base_url: "https://b.example"
      api_key: "sk-beta-old-key-xxxx"
      endpoints:
        gpt-5:
          model: gpt-5-turbo
          max_tokens: 4096
  health:
    cooldown_base: "30s"
    cooldown_max: "5m"
    cooldown_factor: 2

agent:
  primary: "alpha:claude-46"
  fallback_chain:
    - "beta:gpt-5"
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
	build := func(_ string, _ config.ProviderConfig, _ string, _ config.EndpointConfig, _ config.AgentConfig) (*bifrost.Adapter, error) {
		return &bifrost.Adapter{}, nil
	}
	policy := func(_ config.ProviderHealthConfig) (failover.RetryPolicy, failover.RetryPolicy, failover.RetryPolicy) {
		return failover.FastPolicy, failover.MediumPolicy, failover.ProbePolicy
	}
	mgr, err := manager.New(cfg.LLM, cfg.Agent, nil, build, policy, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr, cfgPath, zap.NewNop()), cfgPath
}

func doRequest(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- GET /providers --------------------------------------------------

func TestGet_Providers_Lists_With_NestedEndpoints(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "GET", "/api/v1/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"endpoints"`) {
		t.Fatalf("response missing nested endpoints:\n%s", body)
	}
	if !strings.Contains(body, "claude-46") {
		t.Fatalf("response missing endpoint name:\n%s", body)
	}
	if !strings.Contains(body, "sk-alpha-old-key-xxxx") {
		t.Fatalf("api_key should be plaintext (not masked):\n%s", body)
	}
}

// --- POST /providers (create new provider) -------------------------

func TestPost_Provider_CreatesAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)
	body := `{"name":"deepseek","type":"openai","base_url":"https://api.deepseek.com","api_key":"sk-deepseek"}`
	rec := doRequest(t, h, "POST", "/api/v1/providers", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	d, ok := cfg.LLM.Providers["deepseek"]
	if !ok {
		t.Fatalf("deepseek not persisted to yaml")
	}
	if d.Type != "openai" || d.BaseURL != "https://api.deepseek.com" || d.APIKey != "sk-deepseek" {
		t.Errorf("unexpected fields: %+v", d)
	}
}

func TestPost_Provider_AcceptsGemini(t *testing.T) {
	h, _ := setupTest(t)
	body := `{"name":"google","type":"gemini","base_url":"https://generativelanguage.googleapis.com/v1beta","api_key":"key"}`
	rec := doRequest(t, h, "POST", "/api/v1/providers", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("gemini should be accepted, status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPost_Provider_RejectsUnknownType(t *testing.T) {
	h, _ := setupTest(t)
	body := `{"name":"kimi","type":"kimi","base_url":"x","api_key":"x"}`
	rec := doRequest(t, h, "POST", "/api/v1/providers", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown type", rec.Code)
	}
}

func TestPost_Provider_RejectsDuplicate(t *testing.T) {
	h, _ := setupTest(t)
	body := `{"name":"alpha","type":"openai","base_url":"x","api_key":"x"}`
	rec := doRequest(t, h, "POST", "/api/v1/providers", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for duplicate", rec.Code)
	}
}

func TestPost_Provider_RejectsMissingFields(t *testing.T) {
	h, _ := setupTest(t)
	for _, b := range []string{
		`{"type":"openai"}`,       // missing name
		`{"name":"x"}`,            // missing type
		`{}`,                      // both missing
	} {
		rec := doRequest(t, h, "POST", "/api/v1/providers", b)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", b, rec.Code)
		}
	}
}

// --- PATCH /providers/{p} --------------------------------------------

func TestPatch_ProviderCreds_RotatesAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha",
		`{"api_key":"sk-alpha-NEW"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Providers["alpha"].APIKey != "sk-alpha-NEW" {
		t.Fatalf("api_key not persisted: %s", cfg.LLM.Providers["alpha"].APIKey)
	}
	// Endpoints under alpha survived.
	if len(cfg.LLM.Providers["alpha"].Endpoints) != 2 {
		t.Fatalf("endpoints lost: %+v", cfg.LLM.Providers["alpha"].Endpoints)
	}
}

func TestPatch_ProviderCreds_UnknownTypeRejected(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha", `{"type":"kimi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPatch_ProviderCreds_EmptyRejected(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// --- /providers/{p}/endpoints (collection) ---------------------------

func TestGet_EndpointsList(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "GET", "/api/v1/providers/alpha/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "claude-46") {
		t.Fatalf("response missing claude-46: %s", rec.Body.String())
	}
}

func TestGet_Endpoints_IncludesModelType(t *testing.T) {
	h, _ := setupTest(t)
	// Apply a model_type via PATCH first so we know it's reflected in GET.
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":["vision","tools"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PATCH status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(t, h, "GET", "/api/v1/providers/alpha/endpoints", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Endpoints []struct {
				Name      string   `json:"name"`
				ModelType []string `json:"model_type"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ep := range resp.Data.Endpoints {
		if ep.Name == "claude-46" {
			got = ep.ModelType
			break
		}
	}
	want := []string{"vision", "tools"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("model_type: got %v want %v", got, want)
	}
}

func TestPost_Endpoint_AddsAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints",
		`{"name":"claude-haiku","model":"claude-3-5-haiku"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := cfg.LLM.Providers["alpha"].Endpoints["claude-haiku"]
	if !ok {
		t.Fatalf("claude-haiku not persisted: %+v", cfg.LLM.Providers["alpha"].Endpoints)
	}
	if ep.Model != "claude-3-5-haiku" {
		t.Fatalf("wrong model: %s", ep.Model)
	}
}

func TestPost_Endpoint_MissingFields(t *testing.T) {
	h, _ := setupTest(t)
	for _, body := range []string{`{"name":"only-name"}`, `{"model":"only-model"}`, `{}`} {
		rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

// TestPost_Endpoint_NameWithDotAllowed verifies endpoint names with
// '.' (e.g. "gpt-5.5", "claude-3.5-sonnet") are accepted.
func TestPost_Endpoint_NameWithDotAllowed(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints",
		`{"name":"claude-3.5-sonnet","model":"claude-3-5-sonnet"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
}

// TestPost_Endpoint_NameWithColonRejected verifies ':' is still
// rejected — it's the canonical chain ref separator.
func TestPost_Endpoint_NameWithColonRejected(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints",
		`{"name":"bad:name","model":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// --- /providers/{p}/endpoints/{e} (single) ---------------------------

func TestPatch_Endpoint_UpdatesAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model":"claude-sonnet-4-6-v2","max_tokens":32768}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]
	if ep.Model != "claude-sonnet-4-6-v2" || ep.MaxTokens != 32768 {
		t.Fatalf("not updated: %+v", ep)
	}
}

func TestPatch_Endpoint_AcceptsKnownModelType(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":["vision","tools"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]
	if len(ep.ModelType) != 2 || ep.ModelType[0] != "vision" || ep.ModelType[1] != "tools" {
		t.Errorf("model_type not persisted: %v", ep.ModelType)
	}
}

func TestPatch_Endpoint_RejectsUnknownModelType(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":["rainbow"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_model_type") {
		t.Errorf("error code should mention invalid_model_type: %s", rec.Body.String())
	}
}

func TestPatch_Endpoint_EmptyModelTypeClearsField(t *testing.T) {
	h, cfgPath := setupTest(t)
	// Seed with a value
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":["vision"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status = %d", rec.Code)
	}
	// Now clear
	rec = doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]
	if len(ep.ModelType) != 0 {
		t.Errorf("model_type should be cleared, got %v", ep.ModelType)
	}
}

func TestPatch_Endpoint_OmittedModelTypeUnchanged(t *testing.T) {
	h, cfgPath := setupTest(t)
	// Seed
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"model_type":["vision"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status = %d", rec.Code)
	}
	// Patch a different field
	rec = doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"max_tokens":2048}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second patch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]
	if len(ep.ModelType) != 1 || ep.ModelType[0] != "vision" {
		t.Errorf("model_type should be preserved, got %v", ep.ModelType)
	}
	if ep.MaxTokens != 2048 {
		t.Errorf("max_tokens should be updated, got %d", ep.MaxTokens)
	}
}

func TestDelete_Endpoint_RemovesAndUpdatesChain(t *testing.T) {
	h, cfgPath := setupTest(t)
	// claude-46 is the agent.primary; deleting it should auto-clear
	// primary so beta:gpt-5 (the only fallback) becomes the effective
	// chain head.
	rec := doRequest(t, h, "DELETE", "/api/v1/providers/alpha/endpoints/claude-46", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.LLM.Providers["alpha"].Endpoints["claude-46"]; ok {
		t.Fatalf("claude-46 still in yaml")
	}
	if cfg.Agent.Primary == "alpha:claude-46" {
		t.Fatalf("agent.primary still references deleted endpoint: %q", cfg.Agent.Primary)
	}
	for _, c := range cfg.Agent.FallbackChain {
		if c == "alpha:claude-46" {
			t.Fatalf("fallback_chain still references deleted endpoint: %v", cfg.Agent.FallbackChain)
		}
	}
}

// --- /agent ----------------------------------------------------------

func TestGet_Agent_ReturnsOrderAndHealth(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "GET", "/api/v1/agent", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Data struct {
			Primary       string                    `json:"primary"`
			FallbackChain []string                  `json:"fallback_chain"`
			Entries       []failover.ProviderHealth `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Primary != "alpha:claude-46" {
		t.Fatalf("primary = %q, want alpha:claude-46", resp.Data.Primary)
	}
	if len(resp.Data.FallbackChain) != 1 || resp.Data.FallbackChain[0] != "beta:gpt-5" {
		t.Fatalf("fallback_chain = %v, want [beta:gpt-5]", resp.Data.FallbackChain)
	}
	if resp.Data.Entries[0].Provider != "alpha" || resp.Data.Entries[0].Endpoint != "claude-46" {
		t.Fatalf("entry split wrong: %+v", resp.Data.Entries[0])
	}
}

func TestPatch_Agent_UpdatesPrimaryAndPersists(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/agent",
		`{"primary":"beta:gpt-5","fallback_chain":["alpha:claude-46"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if cfg.Agent.Primary != "beta:gpt-5" {
		t.Fatalf("primary not persisted: %q", cfg.Agent.Primary)
	}
	if len(cfg.Agent.FallbackChain) != 1 || cfg.Agent.FallbackChain[0] != "alpha:claude-46" {
		t.Fatalf("fallback_chain not persisted: %v", cfg.Agent.FallbackChain)
	}
}

func TestPatch_Agent_UpdatesTuning(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/agent",
		`{"max_tokens":12345,"temperature":0.7,"context_window":200000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if cfg.Agent.MaxTokens != 12345 {
		t.Errorf("max_tokens = %d, want 12345", cfg.Agent.MaxTokens)
	}
	if cfg.Agent.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", cfg.Agent.Temperature)
	}
	if cfg.Agent.ContextWindow != 200000 {
		t.Errorf("context_window = %d, want 200000", cfg.Agent.ContextWindow)
	}
}

func TestPatch_Agent_RejectsMalformed(t *testing.T) {
	h, _ := setupTest(t)
	for _, body := range []string{
		`{"primary":"missing-separator"}`,
		`{"primary":"ghost:x"}`,
		`{"fallback_chain":["alpha:ghost-ep"]}`,
		`{"primary":"alpha:claude-46","fallback_chain":["alpha:claude-46"]}`, // duplicates primary
		`{"max_turns":0}`,                                                    // must be ≥ 1
		`{"max_turns":-3}`,
		`{"max_tool_calls":-1}`,                          // must be ≥ 0
		`{"thinking_intensity":"super"}`,                 // not in low/medium/high
		`{}`,
	} {
		rec := doRequest(t, h, "PATCH", "/api/v1/agent", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestPatch_Agent_UpdatesBehaviorLimits(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/agent",
		`{"max_turns":80,"max_tool_calls":120,"thinking_intensity":"high"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if cfg.Agent.MaxTurns != 80 {
		t.Errorf("max_turns = %d, want 80", cfg.Agent.MaxTurns)
	}
	if cfg.Agent.MaxToolCalls != 120 {
		t.Errorf("max_tool_calls = %d, want 120", cfg.Agent.MaxToolCalls)
	}
	if cfg.Agent.ThinkingIntensity != "high" {
		t.Errorf("thinking_intensity = %q, want high", cfg.Agent.ThinkingIntensity)
	}
}

func TestPatch_Agent_ClearsChainWithEmptyArray(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/agent", `{"fallback_chain":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if len(cfg.Agent.FallbackChain) != 0 {
		t.Fatalf("fallback_chain not cleared: %v", cfg.Agent.FallbackChain)
	}
}

func TestAgentSnapshot_TimestampsAreRFC3339(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "GET", "/api/v1/agent", "")
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

func TestRoute_UnknownPath(t *testing.T) {
	h, _ := setupTest(t)
	rec := doRequest(t, h, "GET", "/api/v1/providers/alpha/extra/deep", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPost_Endpoint_PersistsGroup(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints",
		`{"name":"claude-haiku","model":"claude-3-5-haiku","group":"Claude-3.5"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	ep := cfg.LLM.Providers["alpha"].Endpoints["claude-haiku"]
	if ep.Group != "Claude-3.5" {
		t.Errorf("group = %q, want Claude-3.5", ep.Group)
	}
}

func TestPost_Endpoint_OmittedGroupIsEmpty(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "POST", "/api/v1/providers/alpha/endpoints",
		`{"name":"claude-haiku","model":"claude-3-5-haiku"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	cfg, _ := config.Load(cfgPath)
	if got := cfg.LLM.Providers["alpha"].Endpoints["claude-haiku"].Group; got != "" {
		t.Errorf("group = %q, want \"\"", got)
	}
}

func TestPatch_Endpoint_SetsGroup(t *testing.T) {
	h, cfgPath := setupTest(t)
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"group":"Claude-4"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if got := cfg.LLM.Providers["alpha"].Endpoints["claude-46"].Group; got != "Claude-4" {
		t.Errorf("group = %q, want Claude-4", got)
	}
}

func TestPatch_Endpoint_EmptyStringClearsGroup(t *testing.T) {
	h, cfgPath := setupTest(t)
	// seed
	if rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"group":"Claude-4"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d", rec.Code)
	}
	// clear
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"group":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg, _ := config.Load(cfgPath)
	if got := cfg.LLM.Providers["alpha"].Endpoints["claude-46"].Group; got != "" {
		t.Errorf("after clear: group = %q, want \"\"", got)
	}
}

func TestPatch_Endpoint_OmittedGroupUnchanged(t *testing.T) {
	h, cfgPath := setupTest(t)
	// seed
	if rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"group":"Claude-4"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d", rec.Code)
	}
	// patch something else; group must survive
	rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"max_tokens":12345}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	cfg, _ := config.Load(cfgPath)
	if got := cfg.LLM.Providers["alpha"].Endpoints["claude-46"].Group; got != "Claude-4" {
		t.Errorf("group = %q, want Claude-4 (omitted patch should not touch it)", got)
	}
}

func TestGet_Providers_ExposesGroup(t *testing.T) {
	h, _ := setupTest(t)
	// seed via PATCH first
	if rec := doRequest(t, h, "PATCH", "/api/v1/providers/alpha/endpoints/claude-46",
		`{"group":"Claude-4"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d", rec.Code)
	}
	rec := doRequest(t, h, "GET", "/api/v1/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var resp struct {
		Data struct {
			Providers []struct {
				Name      string `json:"name"`
				Endpoints []struct {
					Name  string `json:"name"`
					Group string `json:"group"`
				} `json:"endpoints"`
			} `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var found string
	for _, p := range resp.Data.Providers {
		if p.Name != "alpha" {
			continue
		}
		for _, e := range p.Endpoints {
			if e.Name == "claude-46" {
				found = e.Group
			}
		}
	}
	if found != "Claude-4" {
		t.Errorf("GET providers: alpha.claude-46.group = %q, want Claude-4", found)
	}
}
