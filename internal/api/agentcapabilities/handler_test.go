package agentcapabilities

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

type stubInfo struct {
	key      string
	supports registry.SupportsFlags
}

func (s stubInfo) ActiveModelKey() string                   { return s.key }
func (s stubInfo) ActiveSupports() registry.SupportsFlags   { return s.supports }

func TestAgentCapabilities_ResolvedShape(t *testing.T) {
	info := stubInfo{
		key: "anthropic:claude-opus-4-7",
		supports: registry.SupportsFlags{
			Vision: true, FunctionCalling: true, Reasoning: true, Streaming: true,
		},
	}
	h := New(info, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/capabilities", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			ModelKey     string          `json:"model_key"`
			Supports     map[string]bool `json:"supports"`
			Capabilities []string        `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.ModelKey != "anthropic:claude-opus-4-7" {
		t.Errorf("model_key: %q", resp.Data.ModelKey)
	}
	if !resp.Data.Supports["vision"] || !resp.Data.Supports["function_calling"] {
		t.Errorf("supports: %v", resp.Data.Supports)
	}
	if !resp.Data.Supports["streaming"] {
		t.Errorf("streaming flag missing: %v", resp.Data.Supports)
	}
	caps := resp.Data.Capabilities
	sort.Strings(caps)
	want := []string{"multimodal", "reasoning", "tools"}
	if !reflect.DeepEqual(caps, want) {
		t.Errorf("capabilities: got %v want %v", caps, want)
	}
}

func TestAgentCapabilities_Rejects405(t *testing.T) {
	h := New(stubInfo{}, zap.NewNop())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/capabilities", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d", rec.Code)
	}
}

func TestAgentCapabilities_EmptyChain_DegradedMode(t *testing.T) {
	// No primary configured → empty key + all-false supports + no buckets.
	h := New(stubInfo{}, zap.NewNop())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/capabilities", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Data struct {
			ModelKey     string   `json:"model_key"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Data.ModelKey != "" {
		t.Errorf("expected empty model_key, got %q", resp.Data.ModelKey)
	}
	if len(resp.Data.Capabilities) != 0 {
		t.Errorf("expected no capabilities, got %v", resp.Data.Capabilities)
	}
}
