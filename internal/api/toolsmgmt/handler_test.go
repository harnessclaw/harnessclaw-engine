package toolsmgmt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/tavilysearch"
	"harnessclaw-go/internal/tool/websearch"
)

// newTestHandler builds a Handler with a tool.Registry that already
// has web_search + tavily_search registered from the given cfg. Mirrors
// what cmd/server/main.go does at startup (always register both;
// IsEnabled() decides whether they're effective).
func newTestHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(websearch.New(cfg.Tools.WebSearch, zap.NewNop())); err != nil {
		t.Fatalf("register websearch: %v", err)
	}
	if err := reg.Register(tavilysearch.New(cfg.Tools.TavilySearch, zap.NewNop())); err != nil {
		t.Fatalf("register tavily: %v", err)
	}
	return New(reg, cfg, "" /*cfgPath*/, zap.NewNop())
}

func TestList_ReturnsBothTools(t *testing.T) {
	cfg := &config.Config{Tools: config.ToolsConfig{
		WebSearch:    config.WebSearchConfig{Enabled: false},
		TavilySearch: config.TavilySearchConfig{Enabled: false},
	}}
	h := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var resp struct {
		Code string `json:"code"`
		Data struct {
			Tools []ToolEntry `json:"tools"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "OK" {
		t.Errorf("code: %q", resp.Code)
	}
	if len(resp.Data.Tools) != 2 {
		t.Fatalf("tools count: %d, want 2 (%+v)", len(resp.Data.Tools), resp.Data.Tools)
	}
	// Stable order: tavily_search before web_search alphabetically.
	if resp.Data.Tools[0].Name != "tavily_search" || resp.Data.Tools[1].Name != "web_search" {
		t.Errorf("order: %q, %q", resp.Data.Tools[0].Name, resp.Data.Tools[1].Name)
	}
}

func TestGet_WebSearch_ReturnsCredentials(t *testing.T) {
	cfg := &config.Config{Tools: config.ToolsConfig{
		WebSearch: config.WebSearchConfig{
			Enabled: true,
			APIKey:  "key-1",
			Limit:   5,
		},
	}}
	h := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/web_search", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp struct {
		Code string    `json:"code"`
		Data ToolEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Name != "web_search" {
		t.Errorf("name: %q", resp.Data.Name)
	}
	if resp.Data.RegisteredName != "web_search" {
		t.Errorf("registered_name: %q", resp.Data.RegisteredName)
	}
	if !resp.Data.Enabled {
		t.Error("enabled should be true")
	}
	if !resp.Data.Effective {
		t.Error("effective should be true when credentials are complete")
	}
	if got := resp.Data.Config["api_key"]; got != "key-1" {
		t.Errorf("api_key: %v", got)
	}
	if len(resp.Data.CredentialFields) != 1 {
		t.Errorf("credential_fields count: %d", len(resp.Data.CredentialFields))
	}
}

func TestGet_UnknownTool_404(t *testing.T) {
	cfg := &config.Config{}
	h := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools/bogus", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestList_RejectsPOST(t *testing.T) {
	cfg := &config.Config{}
	h := newTestHandler(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
}

func TestPatch_WebSearch_HotSwapAndPersist(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`tools:
  WebSearch:
    enabled: false
    api_key: "old"
`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := &config.Config{Tools: config.ToolsConfig{
		WebSearch: config.WebSearchConfig{
			Enabled: false,
			APIKey:  "old",
		},
	}}
	reg := tool.NewRegistry()
	if err := reg.Register(websearch.New(cfg.Tools.WebSearch, zap.NewNop())); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := New(reg, cfg, cfgPath, zap.NewNop())

	body := strings.NewReader(`{"enabled":true,"config":{"api_key":"new","limit":7}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tools/web_search", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	// Hot-swap check: registry now holds a tool whose IsEnabled() reflects new credentials.
	got := reg.Get("web_search")
	if got == nil || !got.IsEnabled() {
		t.Errorf("expected hot-swapped enabled tool, got %v", got)
	}
	// In-mem cfg also updated.
	if cfg.Tools.WebSearch.APIKey != "new" {
		t.Errorf("cfg api_key not updated: %q", cfg.Tools.WebSearch.APIKey)
	}
	if !cfg.Tools.WebSearch.Enabled {
		t.Error("cfg enabled not updated")
	}
	// YAML rewritten on disk.
	contents, _ := os.ReadFile(cfgPath)
	for _, frag := range []string{`api_key: "new"`, "enabled: true", "limit: 7"} {
		if !strings.Contains(string(contents), frag) {
			t.Errorf("yaml missing %q, got:\n%s", frag, contents)
		}
	}
}

func TestPatch_RejectsEnableWithoutCredentials(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("tools:\n  WebSearch:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := &config.Config{Tools: config.ToolsConfig{WebSearch: config.WebSearchConfig{}}}
	reg := tool.NewRegistry()
	if err := reg.Register(websearch.New(cfg.Tools.WebSearch, zap.NewNop())); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := New(reg, cfg, cfgPath, zap.NewNop())

	body := strings.NewReader(`{"enabled":true,"config":{}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tools/web_search", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// Registry untouched.
	if reg.Get("web_search").IsEnabled() {
		t.Error("registry should not have been hot-swapped on validation failure")
	}
}

func TestPatch_PartialUpdate_PreservesUnsetFields(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`tools:
  WebSearch:
    enabled: true
    api_key: "keep"
`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := &config.Config{Tools: config.ToolsConfig{
		WebSearch: config.WebSearchConfig{
			Enabled: true,
			APIKey:  "keep",
		},
	}}
	reg := tool.NewRegistry()
	if err := reg.Register(websearch.New(cfg.Tools.WebSearch, zap.NewNop())); err != nil {
		t.Fatalf("register: %v", err)
	}
	h := New(reg, cfg, cfgPath, zap.NewNop())

	// Only update limit; everything else should stay.
	body := strings.NewReader(`{"config":{"limit":42}}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tools/web_search", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if cfg.Tools.WebSearch.APIKey != "keep" {
		t.Errorf("api_key clobbered: %q", cfg.Tools.WebSearch.APIKey)
	}
	if cfg.Tools.WebSearch.Limit != 42 {
		t.Errorf("limit not updated: %d", cfg.Tools.WebSearch.Limit)
	}
	if !cfg.Tools.WebSearch.Enabled {
		t.Error("enabled flipped from omitted body field")
	}
}

func TestPatch_RejectsBadJSON(t *testing.T) {
	cfg := &config.Config{Tools: config.ToolsConfig{}}
	reg := tool.NewRegistry()
	if err := reg.Register(websearch.New(cfg.Tools.WebSearch, zap.NewNop())); err != nil {
		t.Fatalf("register: %v", err)
	}
	tmp := t.TempDir()
	h := New(reg, cfg, filepath.Join(tmp, "config.yaml"), zap.NewNop())

	body := strings.NewReader(`{not json`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/tools/web_search", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d", rec.Code)
	}
}
