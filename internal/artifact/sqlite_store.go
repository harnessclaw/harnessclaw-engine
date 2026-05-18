package artifact

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// blobBase64 is the standard base64 codec used when hydrating
// externally-stored blob bytes back into Artifact.Content. Aliased so a
// future switch to a streaming/url-safe variant is a one-line change.
var blobBase64 = base64.StdEncoding

// SQLiteStore is the persistent Store. Schema mirrors the doc §4 metadata
// fields one-for-one so the on-disk shape and the wire shape never drift.
//
// Storage layout (hybrid):
//   - Text artifacts (file / structured types, or any small Save with
//     non-empty Content) live entirely in the `artifacts.content` column.
//   - Binary artifacts written via SaveInput.BlobBytes — typically
//     type=blob from ArtifactWrite's source_path branch — are streamed
//     to an external file via blobStore. The DB row keeps only the
//     blob_path column (+ size + checksum + preview-as-empty).
//
// Read transparency: Get(...) loads the blob bytes back when blob_path is
// set, base64-encodes them, and fills Artifact.Content so callers don't
// need to know which path was used. This keeps ArtifactRead / ToolResult
// wire shape unchanged from the legacy inline-only era.
type SQLiteStore struct {
	db        *sql.DB
	cfg       Config
	blobStore *BlobStore // nil → external blob path disabled, all writes go inline
}

// NewSQLiteStore opens (or creates) the artifact database at dbPath and
// creates the table if absent. Honours WAL mode for the same reason the
// session store does — many readers, occasional writes.
//
// The companion BlobStore is auto-rooted at <dbPath_dir>/artifact-blobs/
// so external binary artifacts (SaveInput.BlobBytes path) live next to
// the metadata DB. If the blob directory can't be created (permissions),
// the store falls back to inline-only mode and logs the failure once at
// startup — callers can still Save text content, only blob writes return
// errors.
func NewSQLiteStore(dbPath string, cfg Config) (*SQLiteStore, error) {
	if cfg.DefaultTTL == 0 && cfg.PreviewBytes == 0 {
		cfg = DefaultConfig()
	}

	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	const ddl = `CREATE TABLE IF NOT EXISTS artifacts (
		id                  TEXT PRIMARY KEY,
		trace_id            TEXT NOT NULL DEFAULT '',
		session_id          TEXT NOT NULL DEFAULT '',
		type                TEXT NOT NULL DEFAULT '',
		mime_type           TEXT NOT NULL DEFAULT '',
		encoding            TEXT NOT NULL DEFAULT '',
		name                TEXT NOT NULL DEFAULT '',
		description         TEXT NOT NULL DEFAULT '',
		size                INTEGER NOT NULL DEFAULT 0,
		checksum            TEXT NOT NULL DEFAULT '',
		uri                 TEXT NOT NULL DEFAULT '',
		preview             TEXT NOT NULL DEFAULT '',
		schema              TEXT NOT NULL DEFAULT '',
		producer_agent_id   TEXT NOT NULL DEFAULT '',
		producer_run_id     TEXT NOT NULL DEFAULT '',
		producer_task_id    TEXT NOT NULL DEFAULT '',
		consumers           TEXT NOT NULL DEFAULT '[]',
		access              TEXT NOT NULL DEFAULT '{}',
		tags                TEXT NOT NULL DEFAULT '[]',
		version             INTEGER NOT NULL DEFAULT 1,
		parent_artifact_id  TEXT NOT NULL DEFAULT '',
		content             TEXT NOT NULL DEFAULT '',
		blob_path           TEXT NOT NULL DEFAULT '',
		created_at          TEXT NOT NULL,
		expires_at          TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_artifacts_trace ON artifacts(trace_id);
	CREATE INDEX IF NOT EXISTS idx_artifacts_session ON artifacts(session_id);
	CREATE INDEX IF NOT EXISTS idx_artifacts_expires ON artifacts(expires_at);`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("create artifacts table: %w", err)
	}

	// Migrate older DBs that don't have blob_path yet. ALTER TABLE ADD
	// COLUMN is idempotent against new DBs (CREATE TABLE already added it)
	// only when we ignore the "duplicate column" error.
	if _, err := db.Exec(`ALTER TABLE artifacts ADD COLUMN blob_path TEXT NOT NULL DEFAULT ''`); err != nil {
		// Pre-existing column → sqlite returns "duplicate column name".
		// Anything else is a real migration failure.
		if !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("migrate blob_path column: %w", err)
		}
	}

	s := &SQLiteStore{db: db, cfg: cfg}

	// Companion blob store next to the DB file. Failure is non-fatal —
	// the store stays usable for inline writes; blob writes return an
	// error at Save time with a clear message.
	blobDir := filepath.Join(filepath.Dir(dbPath), "artifact-blobs")
	if bs, err := NewBlobStore(blobDir); err == nil {
		s.blobStore = bs
	}
	// (No logger plumbed through; tests use NewMemoryStore which doesn't
	// hit this path. Production main.go can call SetBlobStore if it
	// wants to verify and log the result.)

	return s, nil
}

// SetBlobStore overrides the companion BlobStore. Used by tests and by
// servers that want to log/check the directory at boot.
func (s *SQLiteStore) SetBlobStore(bs *BlobStore) { s.blobStore = bs }

// HasBlobStore reports whether external blob persistence is enabled.
func (s *SQLiteStore) HasBlobStore() bool { return s.blobStore != nil }

// Save implements Store.Save.
func (s *SQLiteStore) Save(ctx context.Context, in *SaveInput) (*Artifact, error) {
	if len(in.Content) > 0 && len(in.BlobBytes) > 0 {
		return nil, fmt.Errorf("artifact: SaveInput.Content and SaveInput.BlobBytes are mutually exclusive — set exactly one")
	}

	now := time.Now().UTC()

	var parent *Artifact
	if in.ParentArtifactID != "" {
		p, err := s.Get(ctx, in.ParentArtifactID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("look up parent: %w", err)
		}
		parent = p
	}

	a := resolveSaveInput(in, s.cfg, parent, now)

	// Blob branch: write binary to BlobStore, record path on the row,
	// keep DB content empty. checksum was already filled by
	// resolveSaveInput from BlobBytes.
	if len(in.BlobBytes) > 0 {
		if s.blobStore == nil {
			return nil, fmt.Errorf("artifact: BlobBytes provided but blob store is not configured")
		}
		path, _, werr := s.blobStore.Write(a.ID, in.BlobBytes)
		if werr != nil {
			return nil, fmt.Errorf("write blob: %w", werr)
		}
		a.BlobPath = path
	}

	accessJSON, err := json.Marshal(a.Access)
	if err != nil {
		return nil, fmt.Errorf("marshal access: %w", err)
	}
	tagsJSON, err := json.Marshal(a.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}
	consumersJSON, err := json.Marshal(a.Consumers)
	if err != nil {
		return nil, fmt.Errorf("marshal consumers: %w", err)
	}

	expiresStr := ""
	if !a.ExpiresAt.IsZero() {
		expiresStr = a.ExpiresAt.Format(time.RFC3339Nano)
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO artifacts
		(id, trace_id, session_id, type, mime_type, encoding,
		 name, description, size, checksum, uri, preview, schema,
		 producer_agent_id, producer_run_id, producer_task_id,
		 consumers, access, tags, version, parent_artifact_id,
		 content, blob_path, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TraceID, a.SessionID, string(a.Type), a.MIMEType, a.Encoding,
		a.Name, a.Description, a.Size, a.Checksum, a.URI, a.Preview,
		string(a.Schema),
		a.Producer.AgentID, a.Producer.AgentRunID, a.Producer.TaskID,
		string(consumersJSON), string(accessJSON), string(tagsJSON),
		a.Version, a.ParentArtifactID,
		a.Content, a.BlobPath,
		a.CreatedAt.Format(time.RFC3339Nano),
		expiresStr,
	)
	if err != nil {
		// Best-effort: if blob was written but DB insert failed, clean up
		// the orphaned blob so the directory doesn't accumulate ghosts.
		if a.BlobPath != "" && s.blobStore != nil {
			_ = s.blobStore.Delete(a.BlobPath)
		}
		return nil, fmt.Errorf("insert artifact: %w", err)
	}
	return cloneArtifact(a), nil
}

// Get implements Store.Get. Transparently re-loads externally-stored blob
// bytes into Artifact.Content (base64-encoded) so callers see a uniform
// shape regardless of where the bytes physically live.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, trace_id, session_id, type, mime_type, encoding,
			name, description, size, checksum, uri, preview, schema,
			producer_agent_id, producer_run_id, producer_task_id,
			consumers, access, tags, version, parent_artifact_id,
			content, blob_path, created_at, expires_at
		 FROM artifacts WHERE id = ?`, id)

	a, err := scanArtifact(row)
	if err != nil {
		return nil, err
	}
	if a.IsExpired(time.Now().UTC()) {
		return nil, ErrNotFound
	}
	if err := s.hydrateBlobContent(a); err != nil {
		return nil, err
	}
	return a, nil
}

// hydrateBlobContent loads BlobPath -> base64-encoded Content when needed.
// Pure read path; safe to call on artifacts that don't use blob storage.
// Errors here are treated as not-found because the blob being missing
// means the artifact is effectively gone (TTL race / disk corruption /
// manual interference). Returning ErrNotFound lets the LLM retry or fall
// back instead of crashing the loop.
func (s *SQLiteStore) hydrateBlobContent(a *Artifact) error {
	if a == nil || a.BlobPath == "" {
		return nil
	}
	if s.blobStore == nil {
		// DB knows the path but the store is gone — refuse rather than
		// silently return empty content.
		return fmt.Errorf("artifact %s has blob_path but blob store is not configured", a.ID)
	}
	data, err := s.blobStore.Read(a.BlobPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("read blob: %w", err)
	}
	// Encode to base64 for wire-shape uniformity. Existing ArtifactRead
	// consumers expect a string; binary callers re-decode at the edge.
	a.Content = blobBase64.EncodeToString(data)
	if a.Encoding == "" {
		a.Encoding = "base64"
	}
	return nil
}

// List implements Store.List.
func (s *SQLiteStore) List(ctx context.Context, filter *ListFilter) ([]*Artifact, error) {
	var (
		where []string
		args  []any
	)
	if filter != nil {
		if filter.TraceID != "" {
			where = append(where, "trace_id = ?")
			args = append(args, filter.TraceID)
		}
		if filter.SessionID != "" {
			where = append(where, "session_id = ?")
			args = append(args, filter.SessionID)
		}
		if filter.AgentID != "" {
			where = append(where, "producer_agent_id = ?")
			args = append(args, filter.AgentID)
		}
	}

	q := `SELECT id, trace_id, session_id, type, mime_type, encoding,
			name, description, size, checksum, uri, preview, schema,
			producer_agent_id, producer_run_id, producer_task_id,
			consumers, access, tags, version, parent_artifact_id,
			content, blob_path, created_at, expires_at
		  FROM artifacts`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query artifacts: %w", err)
	}
	defer rows.Close()

	now := time.Now().UTC()
	var out []*Artifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		if a.IsExpired(now) {
			continue
		}
		// Filter by tag in-process — SQL can't pattern-match a JSON array
		// without JSON1, and we don't want to require the extension.
		if filter != nil && filter.Tag != "" && !hasTag(a.Tags, filter.Tag) {
			continue
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate blob content for blob-stored artifacts. Done after the rows
	// iteration closes so we don't hold a long-lived statement open while
	// reading from disk (sqlite blocks writers on open read statements).
	for _, a := range out {
		if err := s.hydrateBlobContent(a); err != nil {
			// One blob being absent shouldn't void the whole list; skip
			// silently — the caller can re-Get(id) to surface the error
			// on the specific record they care about.
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
	}
	return out, nil
}

// Delete implements Store.Delete. Removes the blob file too when present
// so deleting an artifact frees both DB row and disk bytes.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	// SELECT blob_path first so we can clean the file after the DB row
	// goes away. Doing this BEFORE the DELETE keeps us correct even if
	// the file removal fails — the row is gone and the orphan blob will
	// be cleaned later (or fall to manual cleanup). Doing it AFTER the
	// DELETE risks losing the path if the SELECT fails between them.
	var blobPath string
	if err := s.db.QueryRowContext(ctx,
		"SELECT blob_path FROM artifacts WHERE id = ?", id,
	).Scan(&blobPath); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup blob_path for delete: %w", err)
	}

	res, err := s.db.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if blobPath != "" && s.blobStore != nil {
		_ = s.blobStore.Delete(blobPath) // best-effort, see comment above
	}
	return nil
}

// PurgeExpired implements Store.PurgeExpired. Operates in three steps to
// keep blob filesystem clean alongside the DB:
//
//  1. SELECT id, blob_path FROM expired rows (so we know which files to
//     unlink before the rows go away)
//  2. DELETE the rows
//  3. unlink blob files (best-effort — a failed unlink leaves an orphan
//     but doesn't invalidate the DB purge)
//
// Caller still gets a single-number return matching the legacy contract.
func (s *SQLiteStore) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, blob_path FROM artifacts
		 WHERE expires_at != '' AND expires_at < ?`,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("scan expired: %w", err)
	}
	var (
		expiredIDs  []string
		blobPaths   []string
	)
	for rows.Next() {
		var id, bp string
		if err := rows.Scan(&id, &bp); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired row: %w", err)
		}
		expiredIDs = append(expiredIDs, id)
		if bp != "" {
			blobPaths = append(blobPaths, bp)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(expiredIDs) == 0 {
		return 0, nil
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM artifacts
		 WHERE expires_at != '' AND expires_at < ?`,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired: %w", err)
	}
	n, _ := res.RowsAffected()

	if s.blobStore != nil {
		for _, bp := range blobPaths {
			_ = s.blobStore.Delete(bp)
		}
	}
	return int(n), nil
}

// Close releases the underlying handle.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// scanner is the minimum surface scanArtifact needs. *sql.Row and *sql.Rows
// both satisfy it.
type scanner interface {
	Scan(dest ...any) error
}

// scanArtifact reads one row into an Artifact. Column order MUST match
// the SELECT lists in Get / List.
func scanArtifact(r scanner) (*Artifact, error) {
	var (
		a                                 Artifact
		typeStr                           string
		schemaStr, consumersStr           string
		accessStr, tagsStr                string
		createdStr, expiresStr            string
		size, version                     int
	)
	err := r.Scan(
		&a.ID, &a.TraceID, &a.SessionID, &typeStr, &a.MIMEType, &a.Encoding,
		&a.Name, &a.Description, &size, &a.Checksum, &a.URI, &a.Preview,
		&schemaStr,
		&a.Producer.AgentID, &a.Producer.AgentRunID, &a.Producer.TaskID,
		&consumersStr, &accessStr, &tagsStr,
		&version, &a.ParentArtifactID,
		&a.Content, &a.BlobPath,
		&createdStr, &expiresStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan artifact: %w", err)
	}
	a.Type = Type(typeStr)
	a.Size = size
	a.Version = version
	if schemaStr != "" {
		a.Schema = json.RawMessage(schemaStr)
	}
	if consumersStr != "" {
		_ = json.Unmarshal([]byte(consumersStr), &a.Consumers)
	}
	if accessStr != "" {
		_ = json.Unmarshal([]byte(accessStr), &a.Access)
	}
	if tagsStr != "" {
		_ = json.Unmarshal([]byte(tagsStr), &a.Tags)
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	if expiresStr != "" {
		a.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresStr)
	}
	return &a, nil
}
