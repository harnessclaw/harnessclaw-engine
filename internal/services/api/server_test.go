package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestNewServer_MountsModelsHandler(t *testing.T) {
	called := false
	modelsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	s := NewServer(ServerConfig{Host: "127.0.0.1", Port: 0}, nil, nil, modelsHandler, nil, nil /* toolsHandler */, nil /* videoGenHandler */, nil /* imageGenHandler */, nil /* artifactsHandler */, nil /* capabilitiesHandler */, zap.NewNop())
	srv := httptest.NewServer(s.httpServer.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
	if !called {
		t.Error("modelsHandler was not invoked")
	}
}
