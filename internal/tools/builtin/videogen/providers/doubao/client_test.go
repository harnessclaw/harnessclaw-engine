package doubao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitBuildsArkRequest(t *testing.T) {
	t.Parallel()
	var gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cgt-xyz"})
	}))
	defer srv.Close()

	c := newClient(nil)
	seed := 42
	id, err := c.submit(context.Background(), creds{apiKey: "sk-test", baseURL: srv.URL}, arkSubmitBody{
		Model:    "doubao-seedance-x",
		Prompt:   "a cat",
		ImageURL: "https://img/first.png",
		Ratio:    "9:16",
		Duration: 10,
		Seed:     &seed,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if id != "cgt-xyz" {
		t.Fatalf("id = %q", id)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if !strings.HasSuffix(gotPath, "/contents/generations/tasks") {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"a cat"`) || !strings.Contains(gotBody, `"image_url"`) || !strings.Contains(gotBody, `"seed":42`) {
		t.Fatalf("body missing fields: %s", gotBody)
	}
}

func TestSubmitParsesArkError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"AuthenticationError","message":"bad key"}}`))
	}))
	defer srv.Close()
	c := newClient(nil)
	_, err := c.submit(context.Background(), creds{apiKey: "x", baseURL: srv.URL}, arkSubmitBody{Model: "m", Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("expected ark error message, got %v", err)
	}
}

func TestQueryParsesResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"cgt-1","model":"m","status":"succeeded","updated_at":1000,"content":{"video_url":"https://tos/v.mp4"},"resolution":"720p","ratio":"16:9","duration":5}`))
	}))
	defer srv.Close()
	c := newClient(nil)
	resp, code, err := c.query(context.Background(), creds{apiKey: "x", baseURL: srv.URL}, "cgt-1")
	if err != nil || code != http.StatusOK {
		t.Fatalf("query err=%v code=%d", err, code)
	}
	if resp.Status != "succeeded" || resp.Content == nil || resp.Content.VideoURL != "https://tos/v.mp4" {
		t.Fatalf("parsed resp wrong: %+v", resp)
	}
}
