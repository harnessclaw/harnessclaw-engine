package modelsregistry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider/registry"
)

// TestList_IncludesCapabilities verifies /api/v1/models surfaces the
// derived capabilities array alongside the granular supports flags.
// Front-ends render this as colored chips (multimodal/tools/reasoning/search).
func TestList_IncludesCapabilities(t *testing.T) {
	reg := registry.NewRegistry(&registry.Manifest{
		Providers: map[string]*registry.ProviderSpec{
			"anthropic": {DisplayName: "Anthropic"},
		},
		Models: map[string]*registry.ModelSpec{
			"anthropic/claude-opus-4-7": {
				Provider:    "anthropic",
				ModelID:     "claude-opus-4-7",
				DisplayName: "Claude Opus 4.7",
				Supports: registry.SupportsFlags{
					Vision:          true,
					FunctionCalling: true,
					Reasoning:       true,
				},
			},
		},
	})

	h := New(reg, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("want 1 model, got %d", len(resp.Data))
	}
	got := append([]string(nil), resp.Data[0].Capabilities...)
	sort.Strings(got)
	want := []string{"multimodal", "reasoning", "tools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capabilities: got %v want %v", got, want)
	}
}

// TestList_TextOnlyModelHasNoCapabilities verifies the omitempty
// behavior: text-only models surface no `capabilities` field rather
// than an empty array, which keeps existing client code that ignores
// the field unchanged.
func TestList_TextOnlyModelHasNoCapabilities(t *testing.T) {
	reg := registry.NewRegistry(&registry.Manifest{
		Providers: map[string]*registry.ProviderSpec{"x": {DisplayName: "X"}},
		Models: map[string]*registry.ModelSpec{
			"x/text-only": {Provider: "x", ModelID: "text-only", Supports: registry.SupportsFlags{Streaming: true}},
		},
	})
	h := New(reg, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var raw map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&raw)
	data := raw["data"].([]any)
	first := data[0].(map[string]any)
	if _, has := first["capabilities"]; has {
		t.Errorf("text-only model should omit capabilities, got %v", first["capabilities"])
	}
}
