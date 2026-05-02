package artifact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newSaveInput(content string) *SaveInput {
	return &SaveInput{
		Type:        TypeFile,
		Name:        "test.md",
		Description: "unit-test artifact",
		Content:     content,
		Producer:    Producer{AgentID: "agent_test"},
		TraceID:     "tr_1",
		SessionID:   "sess_1",
	}
}

func TestMemoryStore_SaveAssignsID(t *testing.T) {
	s := NewMemoryStore(DefaultConfig())
	a, err := s.Save(context.Background(), newSaveInput("hello"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !IsValidID(a.ID) {
		t.Errorf("Save returned malformed ID %q", a.ID)
	}
	if a.Size != 5 {
		t.Errorf("Size = %d, want 5", a.Size)
	}
	if a.Checksum == "" || !strings.HasPrefix(a.Checksum, "sha256:") {
		t.Errorf("Checksum should be set; got %q", a.Checksum)
	}
	if a.Version != 1 {
		t.Errorf("Version = %d, want 1", a.Version)
	}
	if a.Access.Scope != ScopeTrace {
		t.Errorf("default scope should be 'trace', got %q", a.Access.Scope)
	}
	if a.URI == "" {
		t.Error("URI should be populated")
	}
}

func TestMemoryStore_GetReturnsCloneNotAlias(t *testing.T) {
	// Mutating the returned artifact must not corrupt the stored copy.
	// Without cloneArtifact, callers could append to Tags and silently
	// taint every subsequent reader.
	s := NewMemoryStore(DefaultConfig())
	a, _ := s.Save(context.Background(), newSaveInput("hello"))

	got, err := s.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Tags = append(got.Tags, "mutated")

	again, _ := s.Get(context.Background(), a.ID)
	for _, tag := range again.Tags {
		if tag == "mutated" {
			t.Fatal("store leaked aliased Tags slice")
		}
	}
}

func TestMemoryStore_GetMissing(t *testing.T) {
	s := NewMemoryStore(DefaultConfig())
	_, err := s.Get(context.Background(), "art_doesnotexist000000000")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown id: want ErrNotFound, got %v", err)
	}
}

func TestMemoryStore_VersioningChainsParent(t *testing.T) {
	// Doc §8: editing means producing a new version with parent_artifact_id.
	// Version must increment; original must remain readable.
	s := NewMemoryStore(DefaultConfig())
	v1, _ := s.Save(context.Background(), newSaveInput("v1 body"))

	in := newSaveInput("v2 body")
	in.ParentArtifactID = v1.ID
	v2, err := s.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2 version = %d, want 2", v2.Version)
	}
	if v2.ParentArtifactID != v1.ID {
		t.Errorf("v2 parent = %q, want %q", v2.ParentArtifactID, v1.ID)
	}
	// Original still readable.
	got, err := s.Get(context.Background(), v1.ID)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if got.Content != "v1 body" {
		t.Errorf("v1 content was overwritten")
	}
}

func TestMemoryStore_TTLExpiry(t *testing.T) {
	// Short TTL → Get after expiry must return ErrNotFound, not a stale
	// record. Doc §9 — janitor or read path must enforce.
	s := NewMemoryStore(Config{DefaultTTL: 5 * time.Millisecond, PreviewBytes: 100})
	a, _ := s.Save(context.Background(), newSaveInput("x"))
	time.Sleep(20 * time.Millisecond)
	if _, err := s.Get(context.Background(), a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired artifact: want ErrNotFound, got %v", err)
	}
}

func TestMemoryStore_PurgeExpired(t *testing.T) {
	s := NewMemoryStore(Config{DefaultTTL: 5 * time.Millisecond, PreviewBytes: 100})
	for i := 0; i < 3; i++ {
		_, _ = s.Save(context.Background(), newSaveInput("x"))
	}
	time.Sleep(20 * time.Millisecond)
	n, err := s.PurgeExpired(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n != 3 {
		t.Errorf("purged %d, want 3", n)
	}
}

func TestMemoryStore_ListFiltersByTrace(t *testing.T) {
	s := NewMemoryStore(DefaultConfig())
	a := newSaveInput("a")
	b := newSaveInput("b")
	b.TraceID = "tr_2"
	_, _ = s.Save(context.Background(), a)
	_, _ = s.Save(context.Background(), b)

	got, err := s.List(context.Background(), &ListFilter{TraceID: "tr_2"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].TraceID != "tr_2" {
		t.Errorf("List trace_id=tr_2 returned %d items, expected just b", len(got))
	}
}

func TestMakePreview_TruncatesOnRuneBoundary(t *testing.T) {
	// "你" is 3 bytes in UTF-8. Cutting mid-rune produces invalid bytes
	// that break downstream JSON encoding of the Preview field. This test
	// catches the regression directly.
	in := strings.Repeat("你好", 100)
	got := MakePreview(in, 7) // 7 bytes = 2 chars + 1 partial; must trim to 6
	if !strings.HasSuffix(got, "…") {
		t.Errorf("preview should end with truncation marker; got %q", got)
	}
	for i := 0; i < len(got); {
		_, size := decodeRune(got[i:])
		if size == 0 {
			t.Fatalf("preview contains invalid utf-8 at byte %d: %q", i, got)
		}
		i += size
	}
}

// decodeRune is the smallest valid-utf8 probe so we don't pull in unicode/utf8
// in the test (the production code already does).
func decodeRune(s string) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	for n := 1; n <= 4 && n <= len(s); n++ {
		if isValidStart(s[0]) && isComplete(s[:n]) {
			return rune(s[0]), n // we don't need the actual rune, just the size
		}
	}
	return 0, 0
}

// isValidStart returns true if b is a UTF-8 lead byte.
func isValidStart(b byte) bool {
	return b < 0x80 || (b >= 0xc2 && b < 0xf5)
}

// isComplete is a coarse "looks like a complete UTF-8 sequence" check —
// good enough for catching mid-rune truncation in the preview test.
func isComplete(s string) bool {
	switch len(s) {
	case 1:
		return s[0] < 0x80
	case 2:
		return s[0] >= 0xc2 && s[0] < 0xe0 && s[1] >= 0x80 && s[1] < 0xc0
	case 3:
		return s[0] >= 0xe0 && s[0] < 0xf0 && s[1] >= 0x80 && s[1] < 0xc0 && s[2] >= 0x80 && s[2] < 0xc0
	}
	return false
}

func TestIsValidID(t *testing.T) {
	a, _ := NewMemoryStore(DefaultConfig()).Save(context.Background(), newSaveInput("x"))
	if !IsValidID(a.ID) {
		t.Errorf("freshly-issued ID %q failed validation", a.ID)
	}
	for _, bad := range []string{"", "bogus", "art_short", "art_NOT_HEX_NOT_HEX_NOT_HE", "id_" + strings.Repeat("a", 24)} {
		if IsValidID(bad) {
			t.Errorf("IsValidID(%q) = true, want false", bad)
		}
	}
}
