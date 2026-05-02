package artifact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is the persistent Store. Schema mirrors the doc §4 metadata
// fields one-for-one so the on-disk shape and the wire shape never drift.
//
// The Content column holds the inline payload for now; once we add an
// object-store backend (doc §11) it will become the URI pointer and
// Content will be empty for blob-sized artifacts.
type SQLiteStore struct {
	db  *sql.DB
	cfg Config
}

// NewSQLiteStore opens (or creates) the artifact database at dbPath and
// creates the table if absent. Honours WAL mode for the same reason the
// session store does — many readers, occasional writes.
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

	return &SQLiteStore{db: db, cfg: cfg}, nil
}

// Save implements Store.Save.
func (s *SQLiteStore) Save(ctx context.Context, in *SaveInput) (*Artifact, error) {
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
		 content, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TraceID, a.SessionID, string(a.Type), a.MIMEType, a.Encoding,
		a.Name, a.Description, a.Size, a.Checksum, a.URI, a.Preview,
		string(a.Schema),
		a.Producer.AgentID, a.Producer.AgentRunID, a.Producer.TaskID,
		string(consumersJSON), string(accessJSON), string(tagsJSON),
		a.Version, a.ParentArtifactID,
		a.Content,
		a.CreatedAt.Format(time.RFC3339Nano),
		expiresStr,
	)
	if err != nil {
		return nil, fmt.Errorf("insert artifact: %w", err)
	}
	return cloneArtifact(a), nil
}

// Get implements Store.Get.
func (s *SQLiteStore) Get(ctx context.Context, id string) (*Artifact, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, trace_id, session_id, type, mime_type, encoding,
			name, description, size, checksum, uri, preview, schema,
			producer_agent_id, producer_run_id, producer_task_id,
			consumers, access, tags, version, parent_artifact_id,
			content, created_at, expires_at
		 FROM artifacts WHERE id = ?`, id)

	a, err := scanArtifact(row)
	if err != nil {
		return nil, err
	}
	if a.IsExpired(time.Now().UTC()) {
		return nil, ErrNotFound
	}
	return a, nil
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
			content, created_at, expires_at
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
	return out, rows.Err()
}

// Delete implements Store.Delete.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeExpired implements Store.PurgeExpired. Operates with a single DELETE
// — much faster than scanning then deleting per row.
func (s *SQLiteStore) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM artifacts
		 WHERE expires_at != '' AND expires_at < ?`,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("purge expired: %w", err)
	}
	n, _ := res.RowsAffected()
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
		&a.Content,
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
