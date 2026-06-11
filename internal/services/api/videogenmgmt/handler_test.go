package videogenmgmt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/config"
	videogen "harnessclaw-go/internal/tools/builtin/videogen"
	"go.uber.org/zap"
)

type agentStub struct{}

func (agentStub) CurrentAgent() config.AgentConfig { return config.AgentConfig{} }

func newHandler(t *testing.T, initial config.VideoGenConfig) (*Handler, *videogen.Source, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  addr: \":8080\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := videogen.NewSource(initial, agentStub{})
	return New(src, cfgPath, zap.NewNop()), src, cfgPath
}

func TestGetVideoGen(t *testing.T) {
	t.Parallel()
	h, _, _ := newHandler(t, config.VideoGenConfig{
		Providers: map[string]config.VideoProviderConfig{
			"doubao": {APIKey: "sk", Endpoints: map[string]config.VideoEndpointConfig{"e": {Model: "m"}}},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/videogen", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "doubao") {
		t.Fatalf("GET body missing provider: %s", rr.Body.String())
	}
}

func TestPatchVideoGenPersistsAndUpdatesSource(t *testing.T) {
	t.Parallel()
	h, src, cfgPath := newHandler(t, config.VideoGenConfig{Providers: map[string]config.VideoProviderConfig{}})
	body := `{"providers":{"doubao":{"api_key":"sk-new","base_url":"https://ark/api/v3","endpoints":{"seedance-lite-i2v":{"model":"doubao-seedance-x"}}}}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/videogen", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH code = %d body=%s", rr.Code, rr.Body.String())
	}
	if ep, ok := src.ResolveEndpoint("doubao:seedance-lite-i2v"); !ok || ep.APIKey != "sk-new" {
		t.Fatalf("source not updated: %+v ok=%v", ep, ok)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "doubao-seedance-x") {
		t.Fatalf("config not persisted:\n%s", raw)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["code"] != "OK" {
		t.Fatalf("envelope code = %v", resp["code"])
	}
}
