package tavilysearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
)

func newTestTool(handler http.HandlerFunc) (*TavilySearchTool, *httptest.Server) {
	srv := httptest.NewServer(handler)
	t := New(config.TavilySearchConfig{
		Enabled:    true,
		APIKey:     "tvly-test-key",
		MaxResults: 3,
	}, zap.NewNop())
	// Override client to point at test server.
	t.client = srv.Client()
	return t, srv
}

func TestName(t *testing.T) {
	tool := New(config.TavilySearchConfig{}, zap.NewNop())
	if tool.Name() != "TavilySearch" {
		t.Errorf("expected 'TavilySearch', got %q", tool.Name())
	}
}

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.TavilySearchConfig
		want    bool
	}{
		{"enabled with key", config.TavilySearchConfig{Enabled: true, APIKey: "key"}, true},
		{"disabled", config.TavilySearchConfig{Enabled: false, APIKey: "key"}, false},
		{"no key", config.TavilySearchConfig{Enabled: true, APIKey: ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := New(tt.cfg, zap.NewNop())
			if got := tool.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateInput(t *testing.T) {
	tool := New(config.TavilySearchConfig{}, zap.NewNop())

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", `{"query":"test"}`, false},
		{"empty query", `{"query":""}`, true},
		{"missing query", `{}`, true},
		{"bad json", `{invalid`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.ValidateInput(json.RawMessage(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInput() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecute_Success(t *testing.T) {
	respBody := `{
		"query": "golang testing",
		"answer": "Go has a built-in testing package.",
		"results": [
			{"title": "Go Testing", "url": "https://go.dev/doc/testing", "content": "The testing package.", "score": 0.95},
			{"title": "Go Blog", "url": "https://go.dev/blog", "content": "Go blog posts.", "score": 0.80}
		],
		"response_time": 0.42
	}`

	tool, srv := newTestTool(func(w http.ResponseWriter, r *http.Request) {
		// Verify request.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tvly-test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var body apiRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Query != "golang testing" {
			t.Errorf("expected query 'golang testing', got %q", body.Query)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(respBody))
	})
	defer srv.Close()

	// Point tool at test server instead of real API.
	origEndpoint := apiEndpoint
	_ = origEndpoint // We can't override const, so we test via the server handler.

	// Override by replacing the execute flow — actually we need to test the real Execute
	// but pointed at our test server. The tool uses apiEndpoint const, so we test the
	// HTTP handling by overriding the client transport.

	// For a proper integration test, use a round-tripper that redirects.
	tool.client.Transport = rewriteTransport{srv.URL}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"golang testing","include_answer":true}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error: %s", result.Content)
	}
	if result.Content == "" {
		t.Error("expected non-empty content")
	}
	// Should contain the answer and results.
	if !contains(result.Content, "Go has a built-in testing package") {
		t.Errorf("expected answer in content, got: %s", result.Content)
	}
	if !contains(result.Content, "Go Testing") {
		t.Errorf("expected result title in content, got: %s", result.Content)
	}
}

func TestExecute_APIError(t *testing.T) {
	tool, srv := newTestTool(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":{"error":"Invalid API key"}}`))
	})
	defer srv.Close()
	tool.client.Transport = rewriteTransport{srv.URL}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"test"}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for 401")
	}
	if !contains(result.Content, "401") {
		t.Errorf("expected status code in error, got: %s", result.Content)
	}
}

func TestExecute_NoResults(t *testing.T) {
	tool, srv := newTestTool(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"query":"obscure","results":[],"response_time":0.1}`))
	})
	defer srv.Close()
	tool.client.Transport = rewriteTransport{srv.URL}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"obscure"}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !contains(result.Content, "No results found") {
		t.Errorf("expected 'No results found', got: %s", result.Content)
	}
}

func TestFormatResults_Snippet(t *testing.T) {
	resp := &apiResponse{
		Answer: "Test answer",
		Results: []apiResult{
			{Title: "Result 1", URL: "https://example.com", Content: "Snippet 1", Score: 0.9},
		},
		ResponseTime: 1.23,
	}
	out := formatResults(resp, false)
	if !contains(out, "Test answer") {
		t.Error("missing answer")
	}
	if !contains(out, "Result 1") {
		t.Error("missing result title")
	}
	if !contains(out, "1.23s") {
		t.Error("missing response time")
	}
	if contains(out, "Content:") {
		t.Error("snippet mode should not have Content: prefix")
	}
}

func TestFormatResults_RawContent(t *testing.T) {
	resp := &apiResponse{
		Results: []apiResult{
			{
				Title:      "Full Article",
				URL:        "https://example.com/article",
				Content:    "Short snippet",
				RawContent: "This is the full article content in markdown.",
				Score:      0.95,
			},
		},
		ResponseTime: 0.5,
	}
	out := formatResults(resp, true)
	if !contains(out, "Content:") {
		t.Error("raw mode should have Content: prefix")
	}
	if !contains(out, "full article content") {
		t.Error("missing raw content body")
	}
	// Snippet should NOT appear when raw is present.
	if contains(out, "Short snippet") {
		t.Error("raw mode should not fall back to snippet")
	}
}

func TestFormatResults_RawContentTruncated(t *testing.T) {
	long := make([]byte, maxRawContentLen+500)
	for i := range long {
		long[i] = 'x'
	}
	resp := &apiResponse{
		Results: []apiResult{
			{Title: "Long", URL: "https://example.com", RawContent: string(long)},
		},
		ResponseTime: 0.1,
	}
	out := formatResults(resp, true)
	if !contains(out, "[truncated]") {
		t.Error("expected truncation marker")
	}
}

// rewriteTransport redirects all requests to the test server.
type rewriteTransport struct {
	baseURL string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[len("http://"):]
	return http.DefaultTransport.RoundTrip(req)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
