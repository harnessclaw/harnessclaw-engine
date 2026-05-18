package artifact

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newBlobTestStore wires a SQLiteStore + BlobStore into a temp dir. Each
// test gets a clean DB + blob directory.
func newBlobTestStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "artifacts.db")
	s, err := NewSQLiteStore(dbPath, DefaultConfig())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if !s.HasBlobStore() {
		t.Fatalf("blob store should be auto-configured next to DB")
	}
	return s, tmp
}

// TestSQLiteStore_BlobRoundTrip is the load-bearing test for the hybrid
// store: save bytes via BlobBytes → file appears in artifact-blobs dir,
// DB content column stays empty, Get hydrates the bytes back identically.
//
// This is the specific failure mode the user hit (docx corrupted by
// LLM base64-copying). Round-trip equality below proves the bytes survive
// the new write path.
func TestSQLiteStore_BlobRoundTrip(t *testing.T) {
	s, tmp := newBlobTestStore(t)
	defer s.Close()

	// Non-ASCII binary payload (mimics a real docx's ZIP header + binary body)
	original := []byte{
		0x50, 0x4B, 0x03, 0x04, 0x0a, 0x00, 0x00, 0x00,
		0xFF, 0x00, 0xAB, 0xCD, 0xEF, 0x12, 0x34, 0x56,
		0xDE, 0xAD, 0xBE, 0xEF,
	}

	saved, err := s.Save(context.Background(), &SaveInput{
		Type:      TypeBlob,
		Name:      "test.docx",
		BlobBytes: original,
		TraceID:   "tr_1",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.BlobPath == "" {
		t.Fatalf("BlobPath should be populated after blob Save")
	}
	if saved.Content != "" {
		t.Errorf("DB content should be empty for blob; got %q", saved.Content)
	}

	// File should exist under <tmp>/artifact-blobs/<id_head>/<id>.bin
	expectedBlobDir := filepath.Join(tmp, "artifact-blobs")
	rel, err := filepath.Rel(expectedBlobDir, saved.BlobPath)
	if err != nil || rel == "" {
		t.Errorf("BlobPath %q should be under %q", saved.BlobPath, expectedBlobDir)
	}
	gotBytes, err := os.ReadFile(saved.BlobPath)
	if err != nil {
		t.Fatalf("read blob file: %v", err)
	}
	if string(gotBytes) != string(original) {
		t.Errorf("blob file bytes differ from original")
	}

	// Get should hydrate Content with base64(original)
	got, err := s.Get(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BlobPath != saved.BlobPath {
		t.Errorf("BlobPath lost on Get: %q vs %q", got.BlobPath, saved.BlobPath)
	}
	decoded, err := base64.StdEncoding.DecodeString(got.Content)
	if err != nil {
		t.Fatalf("base64 decode hydrated content: %v", err)
	}
	if string(decoded) != string(original) {
		t.Errorf("round-trip bytes differ:\n  original: % x\n  got:      % x", original, decoded)
	}
	if got.Encoding != "base64" {
		t.Errorf("Encoding should be 'base64' after hydration, got %q", got.Encoding)
	}
	if got.Size != len(original) {
		t.Errorf("Size = %d, want %d", got.Size, len(original))
	}
}

// TestSQLiteStore_InlineContentStillWorks verifies the legacy text path
// is untouched — non-blob writes go through Content, not BlobBytes.
func TestSQLiteStore_InlineContentStillWorks(t *testing.T) {
	s, _ := newBlobTestStore(t)
	defer s.Close()

	saved, err := s.Save(context.Background(), &SaveInput{
		Type:    TypeFile,
		Name:    "hello.md",
		Content: "# hello world",
		TraceID: "tr_1",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.BlobPath != "" {
		t.Errorf("non-blob save should not produce a BlobPath")
	}
	got, _ := s.Get(context.Background(), saved.ID)
	if got.Content != "# hello world" {
		t.Errorf("file Content round-trip broken: %q", got.Content)
	}
}

// TestSQLiteStore_RejectsBothInputs guards SaveInput's mutual exclusion.
func TestSQLiteStore_RejectsBothInputs(t *testing.T) {
	s, _ := newBlobTestStore(t)
	defer s.Close()
	_, err := s.Save(context.Background(), &SaveInput{
		Type:      TypeBlob,
		Content:   "should not be set",
		BlobBytes: []byte("nope"),
	})
	if err == nil {
		t.Fatal("Save with both Content and BlobBytes should error")
	}
}

// TestSQLiteStore_DeleteRemovesBlob verifies Delete cleans both DB row
// and on-disk file. Without this the blob dir would accumulate orphans.
func TestSQLiteStore_DeleteRemovesBlob(t *testing.T) {
	s, _ := newBlobTestStore(t)
	defer s.Close()

	saved, _ := s.Save(context.Background(), &SaveInput{
		Type:      TypeBlob,
		Name:      "x.bin",
		BlobBytes: []byte{0x01, 0x02, 0x03},
	})
	if _, err := os.Stat(saved.BlobPath); err != nil {
		t.Fatalf("blob file should exist after Save: %v", err)
	}
	if err := s.Delete(context.Background(), saved.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(saved.BlobPath); !os.IsNotExist(err) {
		t.Errorf("blob file should be removed after Delete, stat err=%v", err)
	}
	if _, err := s.Get(context.Background(), saved.ID); err != ErrNotFound {
		t.Errorf("Get after Delete should be ErrNotFound, got %v", err)
	}
}

// TestSQLiteStore_PurgeExpiredCleansBlobs verifies the janitor's purge
// also unlinks blob files. Otherwise the disk would grow unboundedly
// alongside the auto-shrinking sqlite.
func TestSQLiteStore_PurgeExpiredCleansBlobs(t *testing.T) {
	s, _ := newBlobTestStore(t)
	defer s.Close()

	// Save with a 1-microsecond TTL so it's already expired.
	saved, _ := s.Save(context.Background(), &SaveInput{
		Type:      TypeBlob,
		Name:      "ephemeral.bin",
		BlobBytes: []byte{0x42},
		TTL:       1 * time.Microsecond,
	})
	if _, err := os.Stat(saved.BlobPath); err != nil {
		t.Fatalf("blob should exist pre-purge: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // ensure ExpiresAt < now
	n, err := s.PurgeExpired(context.Background(), time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if n == 0 {
		t.Errorf("PurgeExpired returned 0 — expected at least 1")
	}
	if _, err := os.Stat(saved.BlobPath); !os.IsNotExist(err) {
		t.Errorf("blob file should be removed after purge, stat err=%v", err)
	}
}

// TestSQLiteStore_NoBlobStore_RejectsBlobBytes simulates a server that
// failed to set up the companion blob directory. Saves with BlobBytes
// must fail loudly rather than silently lose the bytes.
func TestSQLiteStore_NoBlobStore_RejectsBlobBytes(t *testing.T) {
	s, _ := newBlobTestStore(t)
	s.SetBlobStore(nil)
	defer s.Close()

	_, err := s.Save(context.Background(), &SaveInput{
		Type:      TypeBlob,
		BlobBytes: []byte("hi"),
	})
	if err == nil {
		t.Fatal("Save with BlobBytes but no blob store should error")
	}
}

// TestBlobStore_PathFor_BucketsByIDPrefix verifies the directory split
// to avoid one giant dir.
func TestBlobStore_PathFor_BucketsByIDPrefix(t *testing.T) {
	root := t.TempDir()
	bs, err := NewBlobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := bs.PathFor("art_abc123def")
	if err != nil {
		t.Fatal(err)
	}
	// Should be <root>/ar/art_abc123def.bin
	wantDir := filepath.Join(root, "ar")
	if filepath.Dir(p) != wantDir {
		t.Errorf("bucket dir = %q, want %q", filepath.Dir(p), wantDir)
	}
	if filepath.Base(p) != "art_abc123def.bin" {
		t.Errorf("file name = %q, want art_abc123def.bin", filepath.Base(p))
	}
}

// TestBlobStore_ReadOutsideRootRefused is the security guard: even if a
// poisoned DB row carried a path outside our root, Read must refuse.
func TestBlobStore_ReadOutsideRootRefused(t *testing.T) {
	root := t.TempDir()
	bs, _ := NewBlobStore(root)
	outside := t.TempDir()
	bad := filepath.Join(outside, "secret.txt")
	_ = os.WriteFile(bad, []byte("leak"), 0o600)
	_, err := bs.Read(bad)
	if err == nil {
		t.Fatal("Read should refuse path outside root")
	}
}
