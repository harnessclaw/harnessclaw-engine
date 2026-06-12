package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	imagegen "harnessclaw-go/internal/tools/builtin/imagegen"
	"go.uber.org/zap"
)

func TestGenerateBuildsRequestAndParses(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": "QUJD", "mime_type": "image/png", "revised_prompt": "rp"}},
		})
	}))
	defer srv.Close()
	p := NewProvider("openai", zap.NewNop())
	res, err := p.Generate(context.Background(), imagegen.GenerateRequest{
		Endpoint: imagegen.ImageEndpointRef{Model: "gpt-image-1", APIKey: "sk-test", BaseURL: srv.URL, Path: "/v1/images/generations"},
		Prompt:   "a cat", N: 2, Size: "1024x1024",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(res.Images) != 1 || res.Images[0].B64JSON != "QUJD" || res.Images[0].MIME != "image/png" {
		t.Fatalf("parsed wrong: %+v", res.Images)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if !strings.HasSuffix(gotPath, "/v1/images/generations") {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"a cat"`) || !strings.Contains(gotBody, `"b64_json"`) {
		t.Fatalf("body missing fields: %s", gotBody)
	}
}

func TestGenerateClassifiesErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code     int
		sentinel error
	}{
		{http.StatusUnauthorized, imagegen.ErrPermissionDenied},
		{http.StatusBadRequest, imagegen.ErrValidation},
		{http.StatusInternalServerError, imagegen.ErrTransient},
		{http.StatusTooManyRequests, imagegen.ErrTransient},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(`{"error":{"code":"X","message":"boom"}}`))
		}))
		p := NewProvider("openai", zap.NewNop())
		_, err := p.Generate(context.Background(), imagegen.GenerateRequest{
			Endpoint: imagegen.ImageEndpointRef{Model: "m", APIKey: "k", BaseURL: srv.URL},
			Prompt:   "x",
		})
		srv.Close()
		if err == nil || !errors.Is(err, c.sentinel) {
			t.Fatalf("code %d: expected %v, got %v", c.code, c.sentinel, err)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Fatalf("code %d: message not propagated: %v", c.code, err)
		}
	}
}

func TestProviderName(t *testing.T) {
	t.Parallel()
	if NewProvider("doubao", zap.NewNop()).Name() != "doubao" {
		t.Fatal("name")
	}
}

func TestCredsURL(t *testing.T) {
	t.Parallel()
	cases := []struct{ base, path, want string }{
		{"https://api.openai.com", "/v1/images/generations", "https://api.openai.com/v1/images/generations"}, // split form
		{"https://api.openai.com", "", "https://api.openai.com/v1/images/generations"},                       // bare origin → default path
		{"https://ark.cn-beijing.volces.com/api/v3/images/generations", "", "https://ark.cn-beijing.volces.com/api/v3/images/generations"}, // full URL, path empty
		{"https://x.com/api/v3", "/images/generations", "https://x.com/api/v3/images/generations"},            // split with prefix
	}
	for _, c := range cases {
		got := creds{baseURL: c.base, path: c.path}.url()
		if got != c.want {
			t.Errorf("url(base=%q path=%q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}
