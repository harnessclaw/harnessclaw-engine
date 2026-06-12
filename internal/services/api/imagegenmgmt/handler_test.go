package imagegenmgmt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
	imagegen "harnessclaw-go/internal/tools/builtin/imagegen"
	"go.uber.org/zap"
)

type agentStub struct{}

func (agentStub) CurrentAgent() config.AgentConfig { return config.AgentConfig{} }

func newHandler(t *testing.T, initial config.ImageGenConfig) (*Handler, *imagegen.Source, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  addr: \":8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := imagegen.NewSource(initial, agentStub{})
	return New(src, cfgPath, zap.NewNop()), src, cfgPath
}

func TestGetImageGen(t *testing.T) {
	t.Parallel()
	h, _, _ := newHandler(t, config.ImageGenConfig{
		Providers: map[string]config.ImageProviderConfig{
			"openai": {APIKey: "sk", Path: "/v1/images", Endpoints: map[string]config.ImageEndpointConfig{"e": {Model: "m"}}},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/imagegen", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "openai") {
		t.Fatalf("GET body missing provider: %s", rr.Body.String())
	}
	// Wire contract: the client reads snake_case fields. Go field names
	// leaking into the JSON (APIKey/Endpoints/Model) means missing json
	// tags on the config structs and breaks the settings page.
	for _, want := range []string{`"api_key"`, `"endpoints"`, `"model"`, `"path"`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("GET body missing %s (json tags broken?): %s", want, rr.Body.String())
		}
	}
	for _, bad := range []string{`"APIKey"`, `"Endpoints"`, `"Model"`} {
		if strings.Contains(rr.Body.String(), bad) {
			t.Fatalf("GET body leaks Go field name %s: %s", bad, rr.Body.String())
		}
	}
}

func TestPatchImageGenPersistsAndUpdatesSource(t *testing.T) {
	t.Parallel()
	h, src, cfgPath := newHandler(t, config.ImageGenConfig{Providers: map[string]config.ImageProviderConfig{}})
	body := `{"providers":{"openai":{"api_key":"sk-new","base_url":"https://api.openai.com","path":"/v1/images/generations","endpoints":{"gpt-image":{"model":"gpt-image-1"}}}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/imagegen", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH code = %d body=%s", rr.Code, rr.Body.String())
	}
	if ep, ok := src.ResolveEndpoint("openai:gpt-image"); !ok || ep.APIKey != "sk-new" {
		t.Fatalf("source not updated: %+v ok=%v", ep, ok)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "gpt-image-1") {
		t.Fatalf("config not persisted:\n%s", raw)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["code"] != "OK" {
		t.Fatalf("envelope code = %v", resp["code"])
	}
}

func TestPatchPreservesEndpointsOnApiKeyOnlyPatch(t *testing.T) {
	t.Parallel()
	h, src, _ := newHandler(t, config.ImageGenConfig{
		Providers: map[string]config.ImageProviderConfig{
			"openai": {APIKey: "old", BaseURL: "https://b", Path: "/v1/images", Endpoints: map[string]config.ImageEndpointConfig{"gpt-image": {Model: "m1"}}},
		},
	})
	body := `{"providers":{"openai":{"api_key":"new"}}}` // only api_key, no endpoints
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/imagegen", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH code = %d body=%s", rr.Code, rr.Body.String())
	}
	snap := src.Snapshot()
	p := snap.Providers["openai"]
	if p.APIKey != "new" {
		t.Fatalf("api_key not updated: %q", p.APIKey)
	}
	if p.Endpoints["gpt-image"].Model != "m1" {
		t.Fatalf("endpoints should be preserved on api_key-only patch, got %+v", p.Endpoints)
	}
	if p.BaseURL != "https://b" {
		t.Fatalf("base_url should be preserved, got %q", p.BaseURL)
	}
	if p.Path != "/v1/images" {
		t.Fatalf("path should be preserved, got %q", p.Path)
	}
}
