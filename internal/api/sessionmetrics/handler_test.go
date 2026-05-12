package sessionmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/pkg/types"
)

type fakeStore struct {
	stats map[string]types.SessionStats
}

func (s *fakeStore) LoadSessionStats(_ context.Context, sessionID string) (types.SessionStats, error) {
	return s.stats[sessionID], nil
}

func TestHandler_LiveTrackerWins(t *testing.T) {
	reg := sessionstats.NewRegistry()
	tr := reg.GetOrCreate("sess_live")
	tr.RecordToolCall()

	store := &fakeStore{stats: map[string]types.SessionStats{
		"sess_live": {SessionID: "sess_live", ToolCalls: 999},
	}}

	h := New(reg, store, zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/sess_live/metrics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got types.SessionStats
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1 (live tracker, not DB's 999)", got.ToolCalls)
	}
}

func TestHandler_FallsBackToStore(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &fakeStore{stats: map[string]types.SessionStats{
		"sess_cold": {SessionID: "sess_cold", InputTokens: 500},
	}}
	h := New(reg, store, zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/sess_cold/metrics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got types.SessionStats
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", got.InputTokens)
	}
}

func TestHandler_404OnUnknown(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &fakeStore{stats: map[string]types.SessionStats{}}
	h := New(reg, store, zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/missing/metrics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandler_400OnBadPath(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &fakeStore{}
	h := New(reg, store, zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions//metrics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_405OnPost(t *testing.T) {
	reg := sessionstats.NewRegistry()
	store := &fakeStore{}
	h := New(reg, store, zap.NewNop())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/sessions/anything/metrics", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
