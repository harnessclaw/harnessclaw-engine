package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSaveAndLoad(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	sess := &session.Session{
		ID:        "sess-1",
		State:     session.StateActive,
		Messages: []types.Message{
			{ID: "m1", Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}}, CreatedAt: now},
			{ID: "m2", Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}, CreatedAt: now},
		},
		CreatedAt:         now,
		UpdatedAt:         now,
		ChannelName:       "websocket",
		UserID:            "user-42",
		Metadata:          map[string]any{"key": "value"},
		TotalInputTokens:  100,
		TotalOutputTokens: 50,
	}

	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	loaded, err := store.LoadSession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadSession returned nil")
	}

	if loaded.ID != sess.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, sess.ID)
	}
	if loaded.State != sess.State {
		t.Errorf("State = %q, want %q", loaded.State, sess.State)
	}
	if loaded.ChannelName != sess.ChannelName {
		t.Errorf("ChannelName = %q, want %q", loaded.ChannelName, sess.ChannelName)
	}
	if loaded.UserID != sess.UserID {
		t.Errorf("UserID = %q, want %q", loaded.UserID, sess.UserID)
	}
	if loaded.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens = %d, want 100", loaded.TotalInputTokens)
	}
	if loaded.TotalOutputTokens != 50 {
		t.Errorf("TotalOutputTokens = %d, want 50", loaded.TotalOutputTokens)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(loaded.Messages))
	}
	if loaded.Messages[0].ID != "m1" || loaded.Messages[1].ID != "m2" {
		t.Errorf("message IDs mismatch: got %q, %q", loaded.Messages[0].ID, loaded.Messages[1].ID)
	}
	if loaded.Messages[0].Content[0].Text != "hello" {
		t.Errorf("message content mismatch: got %q", loaded.Messages[0].Content[0].Text)
	}
}

func TestLoadNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	loaded, err := store.LoadSession(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil, got %+v", loaded)
	}
}

func TestDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := &session.Session{
		ID:        "sess-del",
		State:     session.StateActive,
		Messages:  []types.Message{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  map[string]any{},
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := store.DeleteSession(ctx, "sess-del"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	loaded, err := store.LoadSession(ctx, "sess-del")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil after delete, got %+v", loaded)
	}
}

func TestSaveUpsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess := &session.Session{
		ID:        "sess-up",
		State:     session.StateActive,
		Messages:  []types.Message{{ID: "m1", Role: types.RoleUser}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  map[string]any{},
	}
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession (1st): %v", err)
	}

	// Add a message and save again.
	sess.Messages = append(sess.Messages, types.Message{ID: "m2", Role: types.RoleAssistant})
	sess.State = session.StateIdle
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession (2nd): %v", err)
	}

	loaded, err := store.LoadSession(ctx, "sess-up")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages after upsert, got %d", len(loaded.Messages))
	}
	if loaded.State != session.StateIdle {
		t.Errorf("state = %q, want %q", loaded.State, session.StateIdle)
	}
}

func TestListSessions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sessions := []*session.Session{
		{ID: "s1", State: session.StateActive, ChannelName: "ws", UserID: "u1", Messages: []types.Message{{ID: "m1"}}, CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]any{}},
		{ID: "s2", State: session.StateArchived, ChannelName: "ws", UserID: "u2", Messages: []types.Message{}, CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]any{}},
		{ID: "s3", State: session.StateActive, ChannelName: "http", UserID: "u1", Messages: []types.Message{{ID: "m1"}, {ID: "m2"}}, CreatedAt: time.Now(), UpdatedAt: time.Now(), Metadata: map[string]any{}},
	}
	for _, s := range sessions {
		if err := store.SaveSession(ctx, s); err != nil {
			t.Fatalf("SaveSession(%s): %v", s.ID, err)
		}
	}

	// List all.
	all, err := store.ListSessions(ctx, nil)
	if err != nil {
		t.Fatalf("ListSessions(nil): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListSessions(nil) = %d, want 3", len(all))
	}

	// Filter by state.
	active := session.StateActive
	filtered, err := store.ListSessions(ctx, &session.SessionFilter{State: &active})
	if err != nil {
		t.Fatalf("ListSessions(active): %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("ListSessions(active) = %d, want 2", len(filtered))
	}

	// Filter by channel.
	ch := "http"
	filtered, err = store.ListSessions(ctx, &session.SessionFilter{ChannelName: &ch})
	if err != nil {
		t.Fatalf("ListSessions(http): %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("ListSessions(http) = %d, want 1", len(filtered))
	}

	// Verify message count.
	for _, s := range filtered {
		if s.ID == "s3" && s.MessageCount != 2 {
			t.Errorf("s3 MessageCount = %d, want 2", s.MessageCount)
		}
	}
}

func TestListSessionsWithPagination(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		sess := &session.Session{
			ID:        fmt.Sprintf("s%d", i),
			State:     session.StateActive,
			Messages:  []types.Message{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second), // ensure ordering
			Metadata:  map[string]any{},
		}
		if err := store.SaveSession(ctx, sess); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}

	page, err := store.ListSessions(ctx, &session.SessionFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page) != 2 {
		t.Errorf("expected 2 results, got %d", len(page))
	}

	page2, err := store.ListSessions(ctx, &session.SessionFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("expected 2 results (page 2), got %d", len(page2))
	}
}

func TestClose(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close-test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// --- Artifact persistence tests ---

func TestSaveAndLoadArtifacts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := "sess_art_test"

	artStore := artifact.NewStore()
	id1 := artStore.Save("tu_1", "Read", "file content here", map[string]any{"file_path": "/tmp/test.go"})
	id2 := artStore.Save("tu_2", "Bash", "command output", nil)

	// Save artifacts.
	err := store.SaveArtifacts(ctx, sessionID, artStore)
	if err != nil {
		t.Fatalf("SaveArtifacts: %v", err)
	}

	// Load into a fresh store.
	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, sessionID, loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 2 {
		t.Errorf("loaded count = %d, want 2", count)
	}

	// Verify artifact 1.
	art1 := loaded.Get(id1)
	if art1 == nil {
		t.Fatalf("artifact %s not found after load", id1)
	}
	if art1.Content != "file content here" {
		t.Errorf("art1 content = %q, want 'file content here'", art1.Content)
	}
	if art1.ToolName != "Read" {
		t.Errorf("art1 tool_name = %q, want 'Read'", art1.ToolName)
	}
	if art1.ToolUseID != "tu_1" {
		t.Errorf("art1 tool_use_id = %q, want 'tu_1'", art1.ToolUseID)
	}
	if fp, ok := art1.Metadata["file_path"]; !ok || fp != "/tmp/test.go" {
		t.Errorf("art1 metadata file_path = %v", art1.Metadata)
	}

	// Verify artifact 2.
	art2 := loaded.Get(id2)
	if art2 == nil {
		t.Fatalf("artifact %s not found after load", id2)
	}
	if art2.Content != "command output" {
		t.Errorf("art2 content = %q, want 'command output'", art2.Content)
	}

	// Verify GetByToolUse works after restore.
	byToolUse := loaded.GetByToolUse("tu_1")
	if byToolUse == nil || byToolUse.ID != id1 {
		t.Error("GetByToolUse should work after restore")
	}
}

func TestSaveArtifacts_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	artStore := artifact.NewStore()
	err := store.SaveArtifacts(ctx, "sess_empty", artStore)
	if err != nil {
		t.Fatalf("SaveArtifacts empty: %v", err)
	}

	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, "sess_empty", loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 0 {
		t.Errorf("loaded count = %d, want 0", count)
	}
}

func TestSaveArtifacts_Idempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := "sess_idem"

	artStore := artifact.NewStore()
	artStore.Save("tu_1", "Read", "content", nil)

	// Save twice — should not error.
	if err := store.SaveArtifacts(ctx, sessionID, artStore); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := store.SaveArtifacts(ctx, sessionID, artStore); err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, sessionID, loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 1 {
		t.Errorf("loaded count = %d, want 1 (no duplicates)", count)
	}
}

func TestDeleteArtifacts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := "sess_del"

	artStore := artifact.NewStore()
	artStore.Save("tu_1", "Read", "to be deleted", nil)
	if err := store.SaveArtifacts(ctx, sessionID, artStore); err != nil {
		t.Fatalf("SaveArtifacts: %v", err)
	}

	// Delete.
	if err := store.DeleteArtifacts(ctx, sessionID); err != nil {
		t.Fatalf("DeleteArtifacts: %v", err)
	}

	// Load should return 0.
	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, sessionID, loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 0 {
		t.Errorf("loaded count = %d, want 0 after delete", count)
	}
}

func TestLoadArtifacts_WrongSession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	artStore := artifact.NewStore()
	artStore.Save("tu_1", "Read", "session A content", nil)
	if err := store.SaveArtifacts(ctx, "sess_A", artStore); err != nil {
		t.Fatalf("SaveArtifacts: %v", err)
	}

	// Loading for a different session should return 0.
	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, "sess_B", loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 0 {
		t.Errorf("loaded count = %d, want 0 for wrong session", count)
	}
}

func TestSaveArtifacts_LargeContent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID := "sess_large"

	artStore := artifact.NewStore()
	largeContent := make([]byte, 100_000)
	for i := range largeContent {
		largeContent[i] = byte('A' + (i % 26))
	}
	id := artStore.Save("tu_1", "Read", string(largeContent), nil)

	if err := store.SaveArtifacts(ctx, sessionID, artStore); err != nil {
		t.Fatalf("SaveArtifacts: %v", err)
	}

	loaded := artifact.NewStore()
	count, err := store.LoadArtifacts(ctx, sessionID, loaded)
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if count != 1 {
		t.Fatalf("loaded count = %d, want 1", count)
	}

	art := loaded.Get(id)
	if art == nil {
		t.Fatal("artifact not found")
	}
	if art.Size != 100_000 {
		t.Errorf("size = %d, want 100000", art.Size)
	}
	if art.Content != string(largeContent) {
		t.Error("large content was corrupted during save/load")
	}
}
