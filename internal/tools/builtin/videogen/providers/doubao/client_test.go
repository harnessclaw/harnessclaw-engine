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

func TestSubmitBodyWireImagePrecedence(t *testing.T) {
	t.Parallel()
	// both given → image_url wins
	w := arkSubmitBody{Prompt: "p", ImageURL: "u", ImageB64: "data:b64"}.wire()
	gotImg := ""
	for _, c := range w.Content {
		if c.Type == "image_url" && c.ImageURL != nil {
			gotImg = c.ImageURL.URL
		}
	}
	if gotImg != "u" {
		t.Fatalf("both given: expected image_url to win, got %q", gotImg)
	}
	// only b64 → b64 used
	w2 := arkSubmitBody{Prompt: "p", ImageB64: "data:b64"}.wire()
	gotImg2 := ""
	for _, c := range w2.Content {
		if c.Type == "image_url" && c.ImageURL != nil {
			gotImg2 = c.ImageURL.URL
		}
	}
	if gotImg2 != "data:b64" {
		t.Fatalf("b64-only: expected b64 url, got %q", gotImg2)
	}
	// neither → no image content item, text only
	w3 := arkSubmitBody{Prompt: "p"}.wire()
	for _, c := range w3.Content {
		if c.Type == "image_url" {
			t.Fatal("no image given: should have no image_url content item")
		}
	}
}

func TestQueryMalformedBodyIsError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`)) // 200 + garbage
	}))
	defer srv.Close()
	c := newClient(nil)
	_, _, err := c.query(context.Background(), creds{apiKey: "x", baseURL: srv.URL}, "cgt-1")
	if err == nil {
		t.Fatal("malformed 2xx body must return an error (treated as transient), not a nil-error empty result")
	}
}
