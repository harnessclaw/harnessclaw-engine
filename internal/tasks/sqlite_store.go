package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a persistent Store backed by SQLite.
type SQLiteStore struct {
	db  *sql.DB
	mu  sync.Mutex // serializes writes for auto-increment
	seq map[string]int
}

// NewSQLiteStore opens (or creates) a SQLite database at the given path
// and creates the tasks table if it doesn't exist.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Ensure parent directory exists.
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode for better concurrent read performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Create table.
	const ddl = `CREATE TABLE IF NOT EXISTS tasks (
		scope_id   TEXT NOT NULL,
		id         TEXT NOT NULL,
		subject    TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'pending',
		owner      TEXT NOT NULL DEFAULT '',
		active_form TEXT NOT NULL DEFAULT '',
		blocks     TEXT NOT NULL DEFAULT '[]',
		blocked_by TEXT NOT NULL DEFAULT '[]',
		metadata   TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (scope_id, id)
	)`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	// Load current sequence numbers from existing data.
	seq := make(map[string]int)
	rows, err := db.Query("SELECT scope_id, MAX(CAST(id AS INTEGER)) FROM tasks GROUP BY scope_id")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var scope string
			var maxID sql.NullInt64
			if rows.Scan(&scope, &maxID) == nil && maxID.Valid {
				seq[scope] = int(maxID.Int64)
			}
		}
	}

	return &SQLiteStore{db: db, seq: seq}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) Create(_ context.Context, task *Task) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.seq[task.ScopeID]; !ok {
		s.seq[task.ScopeID] = 0
	}
	s.seq[task.ScopeID]++
	task.ID = strconv.Itoa(s.seq[task.ScopeID])
	task.Status = TaskStatusPending
	task.CreatedAt = time.Now()
	task.UpdatedAt = task.CreatedAt

	blocksJSON, _ := json.Marshal(task.Blocks)
	blockedByJSON, _ := json.Marshal(task.BlockedBy)
	metaJSON, _ := json.Marshal(task.Metadata)

	_, err := s.db.Exec(
		`INSERT INTO tasks (scope_id, id, subject, description, status, owner, active_form, blocks, blocked_by, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ScopeID, task.ID, task.Subject, task.Description,
		string(task.Status), task.Owner, task.ActiveForm,
		string(blocksJSON), string(blockedByJSON), string(metaJSON),
		task.CreatedAt.Format(time.RFC3339Nano), task.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	cp := *task
	return &cp, nil
}

func (s *SQLiteStore) Get(_ context.Context, scopeID, id string) (*Task, error) {
	row := s.db.QueryRow(
		`SELECT subject, description, status, owner, active_form, blocks, blocked_by, metadata, created_at, updated_at
		 FROM tasks WHERE scope_id = ? AND id = ?`,
		scopeID, id,
	)
	return scanTask(scopeID, id, row)
}

func (s *SQLiteStore) List(_ context.Context, scopeID string) ([]*Task, error) {
	rows, err := s.db.Query(
		`SELECT id, subject, description, status, owner, active_form, blocks, blocked_by, metadata, created_at, updated_at
		 FROM tasks WHERE scope_id = ? AND status != 'deleted' ORDER BY CAST(id AS INTEGER)`,
		scopeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var result []*Task
	for rows.Next() {
		var t Task
		t.ScopeID = scopeID
		var blocksStr, blockedByStr, metaStr, createdStr, updatedStr string
		if err := rows.Scan(&t.ID, &t.Subject, &t.Description, &t.Status, &t.Owner, &t.ActiveForm,
			&blocksStr, &blockedByStr, &metaStr, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		_ = json.Unmarshal([]byte(blocksStr), &t.Blocks)
		_ = json.Unmarshal([]byte(blockedByStr), &t.BlockedBy)
		_ = json.Unmarshal([]byte(metaStr), &t.Metadata)
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
		result = append(result, &t)
	}
	return result, nil
}

func (s *SQLiteStore) Update(_ context.Context, scopeID, id string, updates *TaskUpdate) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read current state.
	row := s.db.QueryRow(
		`SELECT subject, description, status, owner, active_form, blocks, blocked_by, metadata, created_at, updated_at
		 FROM tasks WHERE scope_id = ? AND id = ?`,
		scopeID, id,
	)
	t, err := scanTask(scopeID, id, row)
	if err != nil {
		return nil, err
	}

	// Apply updates.
	if updates.Subject != nil {
		t.Subject = *updates.Subject
	}
	if updates.Description != nil {
		t.Description = *updates.Description
	}
	if updates.Status != nil {
		t.Status = *updates.Status
	}
	if updates.Owner != nil {
		t.Owner = *updates.Owner
	}
	if updates.ActiveForm != nil {
		t.ActiveForm = *updates.ActiveForm
	}
	if len(updates.AddBlocks) > 0 {
		t.Blocks = append(t.Blocks, updates.AddBlocks...)
	}
	if len(updates.AddBlockedBy) > 0 {
		t.BlockedBy = append(t.BlockedBy, updates.AddBlockedBy...)
	}
	if updates.Metadata != nil {
		if t.Metadata == nil {
			t.Metadata = make(map[string]any)
		}
		for k, v := range updates.Metadata {
			if v == nil {
				delete(t.Metadata, k)
			} else {
				t.Metadata[k] = v
			}
		}
	}
	t.UpdatedAt = time.Now()

	blocksJSON, _ := json.Marshal(t.Blocks)
	blockedByJSON, _ := json.Marshal(t.BlockedBy)
	metaJSON, _ := json.Marshal(t.Metadata)

	_, err = s.db.Exec(
		`UPDATE tasks SET subject=?, description=?, status=?, owner=?, active_form=?,
		 blocks=?, blocked_by=?, metadata=?, updated_at=?
		 WHERE scope_id=? AND id=?`,
		t.Subject, t.Description, string(t.Status), t.Owner, t.ActiveForm,
		string(blocksJSON), string(blockedByJSON), string(metaJSON),
		t.UpdatedAt.Format(time.RFC3339Nano),
		scopeID, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	cp := *t
	return &cp, nil
}

func (s *SQLiteStore) Delete(_ context.Context, scopeID, id string) error {
	res, err := s.db.Exec("DELETE FROM tasks WHERE scope_id = ? AND id = ?", scopeID, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not found in scope %s", id, scopeID)
	}
	return nil
}

// scanTask reads a single task row into a Task struct.
func scanTask(scopeID, id string, row *sql.Row) (*Task, error) {
	var t Task
	t.ScopeID = scopeID
	t.ID = id
	var blocksStr, blockedByStr, metaStr, createdStr, updatedStr string
	if err := row.Scan(&t.Subject, &t.Description, &t.Status, &t.Owner, &t.ActiveForm,
		&blocksStr, &blockedByStr, &metaStr, &createdStr, &updatedStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task %s not found in scope %s", id, scopeID)
		}
		return nil, fmt.Errorf("scan task: %w", err)
	}
	_ = json.Unmarshal([]byte(blocksStr), &t.Blocks)
	_ = json.Unmarshal([]byte(blockedByStr), &t.BlockedBy)
	_ = json.Unmarshal([]byte(metaStr), &t.Metadata)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	return &t, nil
}
