package modelsregistry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider/registry"
)

func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	m, err := registry.DefaultManifest()
	if err != nil {
		t.Fatalf("DefaultManifest: %v", err)
	}
	return registry.NewRegistry(m)
}

func TestHandler_ListModels(t *testing.T) {
	h := New(newRegistry(t), zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) < 15 {
		t.Errorf("expected ≥15 models, got %d", len(body.Data))
	}
	first := body.Data[0]
	for _, k := range []string{"id", "provider", "model_id", "display_name", "modalities", "supports", "limits"} {
		if _, ok := first[k]; !ok {
			t.Errorf("first entry missing field %q: %+v", k, first)
		}
	}
}

func TestHandler_GetSingleModel(t *testing.T) {
	h := New(newRegistry(t), zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/models/deepseek/deepseek-v4-flash")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["id"] != "deepseek/deepseek-v4-flash" {
		t.Errorf("id = %v", body["id"])
	}
	limits, _ := body["limits"].(map[string]any)
	if limits["context_window"].(float64) != 1000000 {
		t.Errorf("context_window wrong: %v", limits["context_window"])
	}
}

func TestHandler_404OnUnknownModel(t *testing.T) {
	h := New(newRegistry(t), zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/models/ghost/foo")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_405OnPost(t *testing.T) {
	h := New(newRegistry(t), zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/models", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
