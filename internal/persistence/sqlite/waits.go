package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"harnessclaw-go/internal/legacy/wait"
)

// WaitStore is the SQLite-backed implementation of wait.Store. It shares
// the database handle with the session Store — same concurrency settings
// (single connection, busy_timeout, WAL) so writes serialise cleanly.
type WaitStore struct {
	db *sql.DB
}

// NewWaitStore wraps an existing *sql.DB (typically obtained from the
// session store's New). The caller is responsible for closing the DB.
func NewWaitStore(db *sql.DB) (*WaitStore, error) {
	const ddl = `CREATE TABLE IF NOT EXISTS pending_waits (
		request_id     TEXT PRIMARY KEY,
		session_id     TEXT NOT NULL,
		trace_id       TEXT NOT NULL DEFAULT '',
		kind           TEXT NOT NULL,
		correlation_id TEXT NOT NULL,
		prompt_frame   BLOB NOT NULL,
		anchor_json    TEXT NOT NULL DEFAULT '{}',
		created_at     TEXT NOT NULL,
		expires_at     TEXT NOT NULL DEFAULT ''
	)`
	if _, err := db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("create pending_waits table: %w", err)
	}
	const idx1 = `CREATE INDEX IF NOT EXISTS idx_pending_waits_session ON pending_waits(session_id)`
	const idx2 = `CREATE INDEX IF NOT EXISTS idx_pending_waits_expires ON pending_waits(expires_at)`
	if _, err := db.Exec(idx1); err != nil {
		return nil, fmt.Errorf("create session index: %w", err)
	}
	if _, err := db.Exec(idx2); err != nil {
		return nil, fmt.Errorf("create expiry index: %w", err)
	}
	return &WaitStore{db: db}, nil
}

// DB exposes the underlying *sql.DB for callers that need to share it
// (e.g. running migrations from the same connection pool).
func (s *WaitStore) DB() *sql.DB { return s.db }

// Save implements wait.Store. INSERT OR REPLACE so re-emitting an
// already-known request_id (e.g. on a deliberate retry) is idempotent.
func (s *WaitStore) Save(ctx context.Context, w wait.PendingWait) error {
	if w.RequestID == "" {
		return fmt.Errorf("wait.Save: RequestID required")
	}
	if w.SessionID == "" {
		return fmt.Errorf("wait.Save: SessionID required")
	}
	if w.Kind == "" {
		return fmt.Errorf("wait.Save: Kind required")
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	anchorJSON, err := json.Marshal(w.Anchor)
	if err != nil {
		return fmt.Errorf("marshal anchor: %w", err)
	}
	expiresStr := ""
	if !w.ExpiresAt.IsZero() {
		expiresStr = w.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO pending_waits
		 (request_id, session_id, trace_id, kind, correlation_id,
		  prompt_frame, anchor_json, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.RequestID, w.SessionID, w.TraceID, string(w.Kind), w.CorrelationID,
		w.PromptFrame, string(anchorJSON),
		w.CreatedAt.UTC().Format(time.RFC3339Nano),
		expiresStr,
	)
	if err != nil {
		return fmt.Errorf("insert pending_wait: %w", err)
	}
	return nil
}

// Get implements wait.Store.
func (s *WaitStore) Get(ctx context.Context, requestID string) (*wait.PendingWait, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT request_id, session_id, trace_id, kind, correlation_id,
		        prompt_frame, anchor_json, created_at, expires_at
		 FROM pending_waits WHERE request_id = ?`, requestID)
	w, err := scanWait(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

// Delete implements wait.Store.
func (s *WaitStore) Delete(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_waits WHERE request_id = ?`, requestID)
	if err != nil {
		return fmt.Errorf("delete pending_wait: %w", err)
	}
	return nil
}

// ListBySession implements wait.Store.
func (s *WaitStore) ListBySession(ctx context.Context, sessionID string) ([]*wait.PendingWait, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT request_id, session_id, trace_id, kind, correlation_id,
		        prompt_frame, anchor_json, created_at, expires_at
		 FROM pending_waits WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("list pending_waits: %w", err)
	}
	defer rows.Close()
	out := make([]*wait.PendingWait, 0)
	for rows.Next() {
		w, err := scanWait(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteExpired implements wait.Store. Empty expires_at means "never
// expires" and is excluded from the sweep.
func (s *WaitStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_waits WHERE expires_at != '' AND expires_at <= ?`,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("delete expired pending_waits: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// scanWait is shared by Get and ListBySession. The scanner argument is
// either row.Scan or rows.Scan — both have the same signature.
func scanWait(scan func(...any) error) (*wait.PendingWait, error) {
	var (
		w           wait.PendingWait
		kindStr     string
		anchorJSON  string
		createdStr  string
		expiresStr  string
	)
	err := scan(
		&w.RequestID, &w.SessionID, &w.TraceID, &kindStr, &w.CorrelationID,
		&w.PromptFrame, &anchorJSON, &createdStr, &expiresStr,
	)
	if err != nil {
		return nil, err
	}
	w.Kind = wait.Kind(kindStr)
	if anchorJSON != "" {
		if err := json.Unmarshal([]byte(anchorJSON), &w.Anchor); err != nil {
			return nil, fmt.Errorf("unmarshal anchor: %w", err)
		}
	}
	w.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	if expiresStr != "" {
		w.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresStr)
	}
	return &w, nil
}
