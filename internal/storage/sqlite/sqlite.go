// Package sqlite provides a SQLite-backed session storage implementation.
// Sessions are persisted to disk so conversations survive server restarts.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/pkg/types"
)

// Store is a SQLite-backed session store.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite database at the given path and
// initialises the sessions table.
//
// Concurrency settings (rationale):
//
//   - PRAGMA journal_mode=WAL — readers don't block writers, writers don't
//     block readers. Without this every read is serialised against any
//     in-flight write.
//   - PRAGMA busy_timeout=5000 — when a write needs the lock and can't
//     acquire it (another connection is writing), SQLite blocks up to 5s
//     internally and retries instead of returning SQLITE_BUSY immediately.
//     This is the single most common SQLite-with-Go misconfiguration:
//     without it, any contention surfaces as a hard error.
//   - PRAGMA synchronous=NORMAL — fsync only at WAL checkpoint, not on
//     every commit. Safe under WAL (durability of the most recent
//     committed transaction may be lost on power failure but the DB
//     remains consistent). Roughly 5-10× write throughput improvement.
//   - SetMaxOpenConns(1) — SQLite serialises writes at the file level
//     anyway. Letting the database/sql pool open multiple connections
//     just creates contention without parallelism. Using a single
//     connection makes the serialisation explicit and predictable.
//   - SetMaxIdleConns(1) / SetConnMaxLifetime(0) — keep the one
//     connection warm; never recycle it.
func New(dbPath string) (*Store, error) {
	// Ensure parent directory exists.
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	// modernc.org/sqlite parses _pragma=key=value DSN parameters and
	// applies them as PRAGMAs on each new connection. busy_timeout MUST
	// be set this way (not via Exec) so it applies to the very first
	// statement on every connection — otherwise a slow Exec on conn-A
	// can race with the PRAGMA on conn-B. We pin to one connection
	// below, but the DSN form is still the safer pattern.
	dsn := dbPath +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite is single-writer at the file level. Multiple Go connections
	// writing concurrently produce SQLITE_BUSY; one connection serialises
	// them through the database/sql mutex instead, which always succeeds.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// Best-effort: re-apply WAL mode via Exec so the change survives an
	// existing journal_mode=DELETE database file (DSN PRAGMA only sets
	// the connection state; WAL mode is a database-wide property).
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	const ddl = `CREATE TABLE IF NOT EXISTS sessions (
		id                  TEXT PRIMARY KEY,
		state               TEXT NOT NULL DEFAULT 'active',
		messages            TEXT NOT NULL DEFAULT '[]',
		created_at          TEXT NOT NULL,
		updated_at          TEXT NOT NULL,
		channel_name        TEXT NOT NULL DEFAULT '',
		user_id             TEXT NOT NULL DEFAULT '',
		metadata            TEXT NOT NULL DEFAULT '{}',
		total_input_tokens  INTEGER NOT NULL DEFAULT 0,
		total_output_tokens INTEGER NOT NULL DEFAULT 0,
		metrics_json        TEXT NOT NULL DEFAULT ''
	)`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	// Best-effort: add the column on databases created by an older binary
	// that didn't include metrics_json. ALTER ... ADD COLUMN is idempotent
	// only by skipping the error when the column already exists, hence the
	// targeted error swallow.
	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN metrics_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("add metrics_json column: %w", err)
		}
	}

	return &Store{db: db}, nil
}

// SaveSession persists a session using INSERT OR REPLACE (upsert).
func (s *Store) SaveSession(_ context.Context, sess *session.Session) error {
	msgs := sess.GetMessages()
	msgsJSON, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("marshal messages: %w", err)
	}

	sess.RLockFields()
	meta := sess.Metadata
	state := sess.State
	createdAt := sess.CreatedAt
	updatedAt := sess.UpdatedAt
	channelName := sess.ChannelName
	userID := sess.UserID
	inputTokens := sess.TotalInputTokens
	outputTokens := sess.TotalOutputTokens
	sess.RUnlockFields()

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO sessions
		 (id, state, messages, created_at, updated_at, channel_name, user_id, metadata, total_input_tokens, total_output_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID,
		string(state),
		string(msgsJSON),
		createdAt.Format(time.RFC3339Nano),
		updatedAt.Format(time.RFC3339Nano),
		channelName,
		userID,
		string(metaJSON),
		inputTokens,
		outputTokens,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

// LoadSession retrieves a session by ID. Returns (nil, nil) if not found.
func (s *Store) LoadSession(_ context.Context, id string) (*session.Session, error) {
	row := s.db.QueryRow(
		`SELECT state, messages, created_at, updated_at, channel_name, user_id, metadata, total_input_tokens, total_output_tokens
		 FROM sessions WHERE id = ?`, id,
	)

	var (
		state, msgsStr, metaStr     string
		createdStr, updatedStr      string
		channelName, userID         string
		inputTokens, outputTokens   int
	)
	err := row.Scan(&state, &msgsStr, &createdStr, &updatedStr, &channelName, &userID, &metaStr, &inputTokens, &outputTokens)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan session: %w", err)
	}

	var msgs []types.Message
	if err := json.Unmarshal([]byte(msgsStr), &msgs); err != nil {
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339Nano, createdStr)
	updatedAt, _ := time.Parse(time.RFC3339Nano, updatedStr)

	sess := &session.Session{
		ID:                id,
		State:             session.State(state),
		Messages:          msgs,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
		ChannelName:       channelName,
		UserID:            userID,
		Metadata:          meta,
		TotalInputTokens:  inputTokens,
		TotalOutputTokens: outputTokens,
	}
	return sess, nil
}

// DeleteSession removes a session by ID.
func (s *Store) DeleteSession(_ context.Context, id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ListSessions returns session summaries matching the given filter.
func (s *Store) ListSessions(_ context.Context, filter *session.SessionFilter) ([]*session.SessionSummary, error) {
	var where []string
	var args []any

	if filter != nil {
		if filter.State != nil {
			where = append(where, "state = ?")
			args = append(args, string(*filter.State))
		}
		if filter.ChannelName != nil {
			where = append(where, "channel_name = ?")
			args = append(args, *filter.ChannelName)
		}
		if filter.UserID != nil {
			where = append(where, "user_id = ?")
			args = append(args, *filter.UserID)
		}
	}

	query := "SELECT id, state, messages, created_at, updated_at, channel_name, user_id FROM sessions"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY updated_at DESC"

	if filter != nil && filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", filter.Offset)
		}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []*session.SessionSummary
	for rows.Next() {
		var (
			id, state, msgsStr         string
			createdStr, updatedStr     string
			channelName, userID        string
		)
		if err := rows.Scan(&id, &state, &msgsStr, &createdStr, &updatedStr, &channelName, &userID); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}

		// Count messages by unmarshaling the JSON array.
		var msgs []json.RawMessage
		_ = json.Unmarshal([]byte(msgsStr), &msgs)

		createdAt, _ := time.Parse(time.RFC3339Nano, createdStr)
		updatedAt, _ := time.Parse(time.RFC3339Nano, updatedStr)

		result = append(result, &session.SessionSummary{
			ID:           id,
			State:        session.State(state),
			MessageCount: len(msgs),
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
			ChannelName:  channelName,
			UserID:       userID,
		})
	}
	return result, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying *sql.DB so callers can mount additional
// schemas (notably WaitStore for the recovery layer) over the same
// connection pool — single-writer SQLite requires this to avoid lock
// contention between session writes and wait writes.
func (s *Store) DB() *sql.DB { return s.db }

// SaveSessionStats serialises stats into the metrics_json column for
// sessionID. Returns an error if the session row does not exist —
// callers should ensure SaveSession has been called first.
func (s *Store) SaveSessionStats(_ context.Context, sessionID string, stats types.SessionStats) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("marshal stats: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET metrics_json = ? WHERE id = ?`,
		string(b), sessionID,
	)
	if err != nil {
		return fmt.Errorf("update metrics_json: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("save stats: session %q not found", sessionID)
	}
	return nil
}

// LoadSessionStats returns the persisted snapshot for sessionID. Empty
// or missing data yields a zero SessionStats with a nil error so the
// HTTP layer can render its own "no data yet" response.
func (s *Store) LoadSessionStats(_ context.Context, sessionID string) (types.SessionStats, error) {
	row := s.db.QueryRow(
		`SELECT metrics_json FROM sessions WHERE id = ?`, sessionID,
	)
	var raw string
	err := row.Scan(&raw)
	if err == sql.ErrNoRows {
		return types.SessionStats{}, nil
	}
	if err != nil {
		return types.SessionStats{}, fmt.Errorf("scan metrics_json: %w", err)
	}
	if raw == "" {
		return types.SessionStats{}, nil
	}
	var out types.SessionStats
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return types.SessionStats{}, fmt.Errorf("unmarshal metrics_json: %w", err)
	}
	return out, nil
}
