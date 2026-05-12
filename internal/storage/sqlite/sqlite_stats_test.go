package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

func newStoreT(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSaveLoadSessionStats_RoundTrip(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()

	sess := &session.Session{
		ID: "sess_abc", State: session.StateActive,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	in := types.SessionStats{
		SessionID:    "sess_abc",
		UpdatedAt:    time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC),
		InputTokens:  100,
		OutputTokens: 50,
		PerModel:     []types.ModelStats{{Model: "opus", InputTokens: 100}},
		SubAgents:    []types.SubAgentStats{{AgentRunID: "r1", Status: "completed"}},
	}
	if err := s.SaveSessionStats(ctx, "sess_abc", in); err != nil {
		t.Fatalf("SaveSessionStats: %v", err)
	}

	out, err := s.LoadSessionStats(ctx, "sess_abc")
	if err != nil {
		t.Fatalf("LoadSessionStats: %v", err)
	}
	if out.SessionID != "sess_abc" || out.InputTokens != 100 ||
		len(out.PerModel) != 1 || out.PerModel[0].Model != "opus" ||
		len(out.SubAgents) != 1 || out.SubAgents[0].AgentRunID != "r1" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestLoadSessionStats_AbsentSessionReturnsZero(t *testing.T) {
	s := newStoreT(t)
	out, err := s.LoadSessionStats(context.Background(), "missing")
	if err != nil {
		t.Fatalf("LoadSessionStats on missing: %v", err)
	}
	if out.SessionID != "" || out.InputTokens != 0 || len(out.PerModel) != 0 {
		t.Errorf("expected zero value, got %+v", out)
	}
}

func TestLoadSessionStats_SessionExistsButNoMetrics(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	sess := &session.Session{
		ID: "sess_no_metrics", State: session.StateActive,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	out, err := s.LoadSessionStats(ctx, "sess_no_metrics")
	if err != nil {
		t.Fatalf("LoadSessionStats: %v", err)
	}
	if out.SessionID != "" || out.InputTokens != 0 {
		t.Errorf("expected zero value when metrics_json is NULL, got %+v", out)
	}
}

func TestSaveSessionStats_MissingSessionReturnsError(t *testing.T) {
	s := newStoreT(t)
	err := s.SaveSessionStats(context.Background(), "nonexistent", types.SessionStats{})
	if err == nil {
		t.Fatal("SaveSessionStats on missing session should error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error message should mention 'not found', got: %v", err)
	}
}
