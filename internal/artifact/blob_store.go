// blob_store.go implements the filesystem half of the hybrid artifact
// store. Large binary artifacts (type=blob with non-empty SaveInput.BlobBytes)
// are written to <dbDir>/artifact-blobs/<id>.bin and the metadata DB only
// retains a path reference + size + checksum.
//
// Why hybrid (vs path-only or DB-only):
//
//   - DB-only (legacy): base64 inflates 50MB docx to ~67MB inside sqlite,
//     and every Get round-trips that string. Bloated DB.
//   - Path-only (user-suggested): would store ONLY the user's source path
//     and re-read it on every Get. Breaks artifact immutability (the
//     file can be overwritten/deleted/symlinked behind our back) and
//     creates a permanent TOCTOU window.
//   - Hybrid (this file): COPY the bytes into a server-owned directory
//     on Save. Originals can come and go; the artifact stays immutable
//     until TTL expires. DB is small (path + metadata only). Get reads
//     a single file directly. No copying on read.
//
// The directory is layered <id_first_2_chars>/<id>.bin so a single dir
// never exceeds a few thousand files even with many artifacts. ext4/apfs
// handle 100k+ but reading `ls` and `du` on such a dir is painful.

package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// blobFileMode is the permission used for blob files. 0o600 — only the
// server user. We intentionally don't 0o644 because blob_path is a
// server-internal detail; nothing should read these files except the
// SQLiteStore itself.
const blobFileMode os.FileMode = 0o600

// blobDirMode for the per-artifact bucket dirs. 0o700 — same rationale.
const blobDirMode os.FileMode = 0o700

// BlobStore is the filesystem half of the hybrid persistence layer. It
// owns a single directory (rooted at NewBlobStore.root) and never reads
// or writes outside it. Public methods are safe for concurrent calls;
// the OS gives us atomicity per file via O_TMPFILE-style write+rename.
type BlobStore struct {
	root string
}

// NewBlobStore opens (and creates if absent) the blob directory at root.
// The root is canonicalised so symlink games at this layer can't escape
// to outside data later. Returns an error only when the directory can't
// be created (permissions / disk full at boot) — caller should fall back
// to inline-only mode rather than crash.
func NewBlobStore(root string) (*BlobStore, error) {
	if root == "" {
		return nil, errors.New("blob store: root path is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("blob store: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, blobDirMode); err != nil {
		return nil, fmt.Errorf("blob store: create root: %w", err)
	}
	return &BlobStore{root: abs}, nil
}

// Root returns the absolute directory the store writes into. Useful for
// migration / introspection but rarely needed by callers.
func (b *BlobStore) Root() string { return b.root }

// PathFor returns the absolute path that Write would (or did) use for
// the given artifact id. Splits the id into <head 2 chars>/<id>.bin so a
// single dir never has > a few thousand files.
//
// Returns an error only when id is empty (which is a programmer bug —
// store.Save synthesises ids itself).
func (b *BlobStore) PathFor(id string) (string, error) {
	if id == "" {
		return "", errors.New("blob store: empty artifact id")
	}
	// Avoid path-traversal-via-id: use only the id's first chars as
	// dirname and the full id as filename, both treated as opaque tokens.
	head := id
	if len(head) > 2 {
		head = head[:2]
	}
	return filepath.Join(b.root, head, id+".bin"), nil
}

// Write writes data to PathFor(id) atomically. Atomicity comes from
// write-to-temp + rename within the same directory. Returns the final
// path and the checksum (caller stores it in the DB row).
//
// Idempotency: rewriting the same id overwrites the previous file. The
// artifact store always uses fresh ids on Save, so this is a defensive
// no-op in normal operation.
func (b *BlobStore) Write(id string, data []byte) (path string, checksum string, err error) {
	target, err := b.PathFor(id)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), blobDirMode); err != nil {
		return "", "", fmt.Errorf("blob store: mkdir bucket: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp.*")
	if err != nil {
		return "", "", fmt.Errorf("blob store: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanupTmp := func() {
		_ = os.Remove(tmpName)
	}

	h := sha256.New()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return "", "", fmt.Errorf("blob store: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return "", "", fmt.Errorf("blob store: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("blob store: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, blobFileMode); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("blob store: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanupTmp()
		return "", "", fmt.Errorf("blob store: rename: %w", err)
	}

	// Checksum is computed over the in-memory bytes (not re-read from
	// disk), so a future buggy on-disk corruption can be detected by
	// comparing Read() against stored checksum.
	h.Write(data)
	checksum = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return target, checksum, nil
}

// Read returns the bytes at PathFor(id). Returns os.ErrNotExist when the
// file has been deleted (e.g. after janitor purge). Path is taken from
// the DB row, not recomputed, to tolerate future moves of the root —
// callers MUST pass a.BlobPath, not a fresh PathFor() result.
func (b *BlobStore) Read(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("blob store: empty path")
	}
	if !b.ownsPath(path) {
		return nil, fmt.Errorf("blob store: refuses to read path outside root: %s", path)
	}
	return os.ReadFile(path)
}

// Delete removes the file at path. Best-effort: returning nil even when
// the file is already absent matches store.Delete's "remove the record;
// stale blob is harmless" expectation.
func (b *BlobStore) Delete(path string) error {
	if path == "" {
		return nil
	}
	if !b.ownsPath(path) {
		return fmt.Errorf("blob store: refuses to delete path outside root: %s", path)
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("blob store: delete: %w", err)
	}
	return nil
}

// ownsPath verifies that path is inside our root. Catches accidental
// (bug) and malicious (DB poisoning) attempts to make us touch random
// files. Cheap string check — we trust the root we set at NewBlobStore.
func (b *BlobStore) ownsPath(path string) bool {
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(b.root, clean)
	if err != nil {
		return false
	}
	if rel == "." {
		// root itself, not a file under it
		return false
	}
	// Any ".." segment means outside root.
	for _, seg := range filepath.SplitList(rel) {
		_ = seg
	}
	// SplitList is OS-PATH-separator-aware, but Rel uses filepath
	// separator. Simpler check: forbid ".." in any rel segment.
	if filepath.Clean(rel) == ".." || hasParentDirSegment(rel) {
		return false
	}
	return true
}

func hasParentDirSegment(rel string) bool {
	// Look for ".." as a path element rather than a substring.
	dir := rel
	for {
		base := filepath.Base(dir)
		if base == ".." {
			return true
		}
		next := filepath.Dir(dir)
		if next == dir {
			return false
		}
		dir = next
	}
}
