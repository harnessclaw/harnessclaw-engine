package artifact

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db := filepath.Join(t.TempDir(), "art.db")
	s, err := NewSQLiteStore(db, DefaultConfig())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLiteStore_SaveLoadRoundtrip(t *testing.T) {
	// Every doc §4 metadata field must round-trip through the DB without
	// loss — otherwise downstream agents see incomplete records.
	s := newTestSQLiteStore(t)
	in := &SaveInput{
		Type:        TypeStructured,
		MIMEType:    "application/json",
		Encoding:    "utf-8",
		Name:        "report.json",
		Description: "round-trip test",
		Content:     `{"month":"2024-01","sales":950000}`,
		Schema:      []byte(`{"type":"table","columns":["month","sales"]}`),
		Tags:        []string{"sales", "2024"},
		Producer: Producer{
			AgentID:    "agent_a",
			AgentRunID: "run_1",
			TaskID:     "s1",
		},
		TraceID:   "tr_x",
		SessionID: "sess_x",
	}

	saved, err := s.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	checks := []struct {
		name string
		ok   bool
	}{
		{"Type", got.Type == TypeStructured},
		{"MIMEType", got.MIMEType == "application/json"},
		{"Encoding", got.Encoding == "utf-8"},
		{"Name", got.Name == "report.json"},
		{"Description", got.Description == "round-trip test"},
		{"Content", got.Content == in.Content},
		{"Size", got.Size == len(in.Content)},
		{"Checksum", got.Checksum == saved.Checksum},
		{"Preview", got.Preview != ""},
		{"Schema", string(got.Schema) == string(in.Schema)},
		{"Producer.AgentID", got.Producer.AgentID == "agent_a"},
		{"Producer.AgentRunID", got.Producer.AgentRunID == "run_1"},
		{"Producer.TaskID", got.Producer.TaskID == "s1"},
		{"TraceID", got.TraceID == "tr_x"},
		{"SessionID", got.SessionID == "sess_x"},
		{"TagsLen", len(got.Tags) == 2},
		{"Version", got.Version == 1},
		{"AccessScope", got.Access.Scope == ScopeTrace},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("round-trip lost %s: %+v", c.name, got)
		}
	}
}

func TestSQLiteStore_GetMissing(t *testing.T) {
	s := newTestSQLiteStore(t)
	_, err := s.Get(context.Background(), "art_doesnotexist000000000")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown id: want ErrNotFound, got %v", err)
	}
}

func TestSQLiteStore_VersioningPersistsParentLink(t *testing.T) {
	s := newTestSQLiteStore(t)
	v1, _ := s.Save(context.Background(), &SaveInput{
		Type:    TypeFile,
		Content: "v1",
		Producer: Producer{AgentID: "a"},
	})
	v2, err := s.Save(context.Background(), &SaveInput{
		Type:             TypeFile,
		Content:          "v2",
		ParentArtifactID: v1.ID,
		Producer:         Producer{AgentID: "a"},
	})
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if v2.Version != 2 || v2.ParentArtifactID != v1.ID {
		t.Errorf("v2 chain broken: version=%d parent=%q", v2.Version, v2.ParentArtifactID)
	}
}

func TestSQLiteStore_PurgeExpired(t *testing.T) {
	// Use a short DefaultTTL so we don't sleep long in CI.
	db := filepath.Join(t.TempDir(), "art.db")
	s, err := NewSQLiteStore(db, Config{DefaultTTL: 10 * time.Millisecond, PreviewBytes: 100})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		_, _ = s.Save(context.Background(), &SaveInput{
			Type:     TypeFile,
			Content:  "x",
			Producer: Producer{AgentID: "a"},
		})
	}
	time.Sleep(40 * time.Millisecond)
	n, err := s.PurgeExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 3 {
		t.Errorf("purged %d, want 3", n)
	}
}

func TestSQLiteStore_ListFiltersByTraceAndTag(t *testing.T) {
	s := newTestSQLiteStore(t)
	mk := func(trace string, tags []string) {
		_, _ = s.Save(context.Background(), &SaveInput{
			Type:     TypeFile,
			Content:  "x",
			Tags:     tags,
			TraceID:  trace,
			Producer: Producer{AgentID: "a"},
		})
	}
	mk("tr_1", []string{"sales"})
	mk("tr_1", []string{"hr"})
	mk("tr_2", []string{"sales"})

	got, err := s.List(context.Background(), &ListFilter{TraceID: "tr_1", Tag: "sales"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("filter trace+tag returned %d, want 1", len(got))
	}
}

func TestSQLiteStore_DeleteRemoves(t *testing.T) {
	s := newTestSQLiteStore(t)
	a, _ := s.Save(context.Background(), &SaveInput{
		Type:     TypeFile,
		Content:  "x",
		Producer: Producer{AgentID: "a"},
	})
	if err := s.Delete(context.Background(), a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(context.Background(), a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Get should return ErrNotFound, got %v", err)
	}
}
