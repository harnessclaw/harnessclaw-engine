package toolsmgmt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/tavilysearch"
	"harnessclaw-go/internal/tool/websearch"
)

// newTestHandler builds a Handler with a tool.Registry that already
// has WebSearch + TavilySearch registered from the given cfg. Mirrors
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
			Enabled:   true,
			APIKey:    "key-1",
			APISecret: "secret-1",
			AppID:     "app-1",
			Host:      "search.example.com",
			Path:      "/biz/search",
			Limit:     5,
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
	if resp.Data.RegisteredName != "WebSearch" {
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
	if len(resp.Data.CredentialFields) != 3 {
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
