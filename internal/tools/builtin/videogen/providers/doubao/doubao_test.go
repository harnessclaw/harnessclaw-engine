package doubao

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	videogen "harnessclaw-go/internal/tools/builtin/videogen"
	"go.uber.org/zap"
)

func TestProviderName(t *testing.T) {
	t.Parallel()
	if NewProvider("doubao", zap.NewNop()).Name() != "doubao" {
		t.Fatal("name must be doubao")
	}
}

func TestProviderSubmitAndQuery(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"cgt-99"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"cgt-99","model":"m","status":"succeeded","updated_at":1000,"content":{"video_url":"https://tos/v.mp4"},"resolution":"720p","ratio":"16:9","duration":5}`))
	}))
	defer srv.Close()
	p := NewProvider("doubao", zap.NewNop())

	ep := videogen.EndpointRef{Provider: "doubao", Endpoint: "e", Model: "m", APIKey: "sk", BaseURL: srv.URL}
	sub, err := p.SubmitTask(context.Background(), videogen.SubmitRequest{Endpoint: ep, Prompt: "hi", DurationS: 5, AspectRatio: "16:9"})
	if err != nil || sub.TaskID != "cgt-99" {
		t.Fatalf("submit: %+v err=%v", sub, err)
	}

	q, err := p.QueryTask(context.Background(), videogen.QueryRequest{Endpoint: ep, TaskID: "cgt-99"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if q.Status != videogen.StatusSucceeded || q.VideoURL != "https://tos/v.mp4" {
		t.Fatalf("query result wrong: %+v", q)
	}
	wantExpiry := time.Unix(1000, 0).Add(24 * time.Hour)
	if !q.URLExpiresAt.Equal(wantExpiry) {
		t.Fatalf("URLExpiresAt = %v, want %v", q.URLExpiresAt, wantExpiry)
	}
}

func TestProviderQueryNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NotFound","message":"no task"}}`))
	}))
	defer srv.Close()
	p := NewProvider("doubao", zap.NewNop())
	ep := videogen.EndpointRef{Provider: "doubao", APIKey: "sk", BaseURL: srv.URL}
	q, err := p.QueryTask(context.Background(), videogen.QueryRequest{Endpoint: ep, TaskID: "missing"})
	if err != nil {
		t.Fatalf("404 should map to NotFound status, not error: %v", err)
	}
	if q.Status != videogen.StatusNotFound {
		t.Fatalf("expected NotFound, got %v", q.Status)
	}
}
