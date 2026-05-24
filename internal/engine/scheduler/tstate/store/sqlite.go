package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
)

const tstateDDL = `
CREATE TABLE IF NOT EXISTS tstate_tasks (
  id                TEXT PRIMARY KEY,
  team_id           TEXT NOT NULL DEFAULT '',
  session_id        TEXT NOT NULL DEFAULT '',
  parent_id         TEXT NOT NULL DEFAULT '',
  kind              TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL,
  priority          INTEGER NOT NULL DEFAULT 0,
  attempt           INTEGER NOT NULL DEFAULT 0,
  spawn_depth       INTEGER NOT NULL DEFAULT 0,
  input_ref         TEXT NOT NULL DEFAULT '',
  result_ref        TEXT NOT NULL DEFAULT '',
  staged_result_ref TEXT NOT NULL DEFAULT '',
  checkpoint_ref    TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',
  failed_reason     TEXT NOT NULL DEFAULT '',
  lease_worker      TEXT NOT NULL DEFAULT '',
  lease_expires     INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL DEFAULT 0,
  started_at        INTEGER NOT NULL DEFAULT 0,
  finished_at       INTEGER NOT NULL DEFAULT 0,
  deps              TEXT NOT NULL DEFAULT '[]',
  waiting_for       TEXT NOT NULL DEFAULT '[]',
  resource_req      TEXT NOT NULL DEFAULT '{}',
  budget            TEXT NOT NULL DEFAULT '{}',
  leaf_spec         TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_tstate_status     ON tstate_tasks(team_id, status);
CREATE INDEX IF NOT EXISTS idx_tstate_parent     ON tstate_tasks(parent_id);
CREATE INDEX IF NOT EXISTS idx_tstate_status_pri ON tstate_tasks(team_id, status, priority DESC, created_at ASC);
`

// ErrCASConflict is returned by CAS when the current status doesn't match expect.
var ErrCASConflict = errors.New("tstate/store: CAS conflict")

// ErrNotFound is returned when a row is not found.
var ErrNotFound = errors.New("tstate/store: not found")

// SQLite is a durable tstate.Store backed by a shared *sql.DB.
// The caller owns the *sql.DB lifetime; Close() is a no-op.
type SQLite struct {
	db *sql.DB
}

// NewSQLite wraps an existing *sql.DB and runs the tstate schema migration.
func NewSQLite(db *sql.DB) (tstate.Store, error) {
	s := &SQLite{db: db}
	if _, err := db.Exec(tstateDDL); err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite migrate: %w", err)
	}
	return s, nil
}

// Close is a no-op: the caller owns *sql.DB.
func (s *SQLite) Close() error { return nil }

// Insert inserts a new TaskState row. Returns error on duplicate ID.
func (s *SQLite) Insert(_ context.Context, ts tstate.TaskState) error {
	deps, wf, rr, bud, ls, err := marshalJSON(ts)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO tstate_tasks
		 (id, team_id, session_id, parent_id, kind, status,
		  priority, attempt, spawn_depth,
		  input_ref, result_ref, staged_result_ref, checkpoint_ref,
		  last_error, failed_reason,
		  lease_worker, lease_expires,
		  created_at, started_at, finished_at,
		  deps, waiting_for, resource_req, budget, leaf_spec)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(ts.ID),
		string(ts.TeamID),
		ts.SessionID,
		string(ts.ParentID),
		string(ts.Kind),
		string(ts.Status),
		ts.Priority,
		ts.Attempt,
		ts.SpawnDepth,
		string(ts.InputRef),
		string(ts.ResultRef),
		string(ts.StagedResultRef),
		string(ts.CheckpointRef),
		ts.LastError,
		string(ts.FailedReason),
		ts.Lease.WorkerID,
		ts.Lease.ExpiresAt.UnixNano(),
		ts.CreatedAt.UnixNano(),
		ts.StartedAt.UnixNano(),
		ts.FinishedAt.UnixNano(),
		deps, wf, rr, bud, ls,
	)
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite insert %s: %w", ts.ID, err)
	}
	return nil
}

// Delete removes a row by ID. Returns an error if the row does not exist.
func (s *SQLite) Delete(_ context.Context, id types.TaskID) error {
	res, err := s.db.Exec(`DELETE FROM tstate_tasks WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite delete %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("tstate/store/sqlite delete %s: %w", id, ErrNotFound)
	}
	return nil
}

// Get fetches a single row by ID.
func (s *SQLite) Get(_ context.Context, id types.TaskID) (tstate.TaskState, error) {
	row := s.db.QueryRow(`SELECT `+selectCols+` FROM tstate_tasks WHERE id = ?`, string(id))
	ts, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return tstate.TaskState{}, fmt.Errorf("tstate/store/sqlite get %s: %w", id, ErrNotFound)
	}
	return ts, err
}

// CAS performs a compare-and-swap on the status field, then applies mut.
func (s *SQLite) CAS(_ context.Context, id types.TaskID, expect, set types.Status, mut tstate.Mutation) error {
	return casOnDB(s.db, id, expect, set, mut)
}

// UpdateField updates a single named field with an attempt-epoch guard.
func (s *SQLite) UpdateField(_ context.Context, id types.TaskID, field string, value any, attemptGuard int) error {
	switch field {
	case tstate.FieldStagedResultRef:
		v, ok := value.(types.Ref)
		if !ok {
			return errors.New("tstate/store/sqlite: staged_result_ref must be types.Ref")
		}
		res, err := s.db.Exec(
			`UPDATE tstate_tasks SET staged_result_ref = ? WHERE id = ? AND attempt = ?`,
			string(v), string(id), attemptGuard,
		)
		if err != nil {
			return fmt.Errorf("tstate/store/sqlite UpdateField %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("tstate/store/sqlite UpdateField %s: epoch mismatch or not found", id)
		}
		return nil
	default:
		return fmt.Errorf("tstate/store/sqlite: UpdateField rejected field=%q", field)
	}
}

// ListByStatus returns tasks matching team+status, ordered by priority DESC, created_at ASC.
func (s *SQLite) ListByStatus(_ context.Context, team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error) {
	q := `SELECT ` + selectCols + ` FROM tstate_tasks WHERE status = ?`
	args := []any{string(st)}
	if team != "" {
		q += ` AND team_id = ?`
		args = append(args, string(team))
	}
	q += ` ORDER BY priority DESC, created_at ASC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite ListByStatus: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListByParent returns all tasks with the given parent_id.
func (s *SQLite) ListByParent(_ context.Context, parent types.TaskID) ([]tstate.TaskState, error) {
	rows, err := s.db.Query(
		`SELECT `+selectCols+` FROM tstate_tasks WHERE parent_id = ?`,
		string(parent),
	)
	if err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite ListByParent: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListPendingDependentOn returns pending tasks whose deps list contains depID.
func (s *SQLite) ListPendingDependentOn(_ context.Context, depID types.TaskID) ([]tstate.TaskState, error) {
	rows, err := s.db.Query(
		`SELECT `+selectCols+` FROM tstate_tasks WHERE status = ?`,
		string(types.StatusPending),
	)
	if err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite ListPendingDependentOn: %w", err)
	}
	defer rows.Close()

	all, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	var out []tstate.TaskState
	for _, ts := range all {
		for _, d := range ts.Deps {
			if d == depID {
				out = append(out, ts)
				break
			}
		}
	}
	return out, nil
}

// InTx runs fn inside a database transaction. Rolls back on error.
func (s *SQLite) InTx(ctx context.Context, fn func(tstate.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite BeginTx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := fn(&sqliteTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tstate/store/sqlite Commit: %w", err)
	}
	return nil
}

// --- sqliteTx: implements tstate.Tx ---

type sqliteTx struct{ tx *sql.Tx }

func (t *sqliteTx) Get(id types.TaskID) (tstate.TaskState, error) {
	row := t.tx.QueryRow(`SELECT `+selectCols+` FROM tstate_tasks WHERE id = ?`, string(id))
	ts, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return tstate.TaskState{}, fmt.Errorf("tstate/store/sqlite tx.Get %s: %w", id, ErrNotFound)
	}
	return ts, err
}

func (t *sqliteTx) ListChildren(parent types.TaskID) ([]tstate.TaskState, error) {
	rows, err := t.tx.Query(
		`SELECT `+selectCols+` FROM tstate_tasks WHERE parent_id = ?`,
		string(parent),
	)
	if err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite tx.ListChildren: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

func (t *sqliteTx) ListByStatus(team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error) {
	q := `SELECT ` + selectCols + ` FROM tstate_tasks WHERE status = ?`
	args := []any{string(st)}
	if team != "" {
		q += ` AND team_id = ?`
		args = append(args, string(team))
	}
	q += ` ORDER BY priority DESC, created_at ASC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := t.tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("tstate/store/sqlite tx.ListByStatus: %w", err)
	}
	defer rows.Close()
	return scanRows(rows)
}

func (t *sqliteTx) CAS(id types.TaskID, expect, set types.Status, mut tstate.Mutation) error {
	return casOnTx(t.tx, id, expect, set, mut)
}

func (t *sqliteTx) Insert(ts tstate.TaskState) error {
	deps, wf, rr, bud, ls, err := marshalJSON(ts)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(
		`INSERT INTO tstate_tasks
		 (id, team_id, session_id, parent_id, kind, status,
		  priority, attempt, spawn_depth,
		  input_ref, result_ref, staged_result_ref, checkpoint_ref,
		  last_error, failed_reason,
		  lease_worker, lease_expires,
		  created_at, started_at, finished_at,
		  deps, waiting_for, resource_req, budget, leaf_spec)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(ts.ID),
		string(ts.TeamID),
		ts.SessionID,
		string(ts.ParentID),
		string(ts.Kind),
		string(ts.Status),
		ts.Priority,
		ts.Attempt,
		ts.SpawnDepth,
		string(ts.InputRef),
		string(ts.ResultRef),
		string(ts.StagedResultRef),
		string(ts.CheckpointRef),
		ts.LastError,
		string(ts.FailedReason),
		ts.Lease.WorkerID,
		ts.Lease.ExpiresAt.UnixNano(),
		ts.CreatedAt.UnixNano(),
		ts.StartedAt.UnixNano(),
		ts.FinishedAt.UnixNano(),
		deps, wf, rr, bud, ls,
	)
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite tx.Insert %s: %w", ts.ID, err)
	}
	return nil
}

func (t *sqliteTx) Delete(id types.TaskID) error {
	res, err := t.tx.Exec(`DELETE FROM tstate_tasks WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite tx.Delete %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("tstate/store/sqlite tx.Delete %s: %w", id, ErrNotFound)
	}
	return nil
}

// --- CAS helpers ---

// execer abstracts *sql.DB and *sql.Tx for CAS logic.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func casOnDB(db *sql.DB, id types.TaskID, expect, set types.Status, mut tstate.Mutation) error {
	return casExec(db, id, expect, set, mut)
}

func casOnTx(tx *sql.Tx, id types.TaskID, expect, set types.Status, mut tstate.Mutation) error {
	return casExec(tx, id, expect, set, mut)
}

func casExec(ex execer, id types.TaskID, expect, set types.Status, mut tstate.Mutation) error {
	// Read the current row first, then build the full UPDATE with mutations applied.
	row := ex.QueryRow(`SELECT `+selectCols+` FROM tstate_tasks WHERE id = ?`, string(id))
	ts, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("tstate/store/sqlite CAS %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if ts.Status != expect {
		return fmt.Errorf("tstate/store/sqlite CAS %s: want=%s got=%s: %w", id, expect, ts.Status, ErrCASConflict)
	}

	// Apply the mutation to the in-memory copy.
	ts.Status = set
	applyMutationSQLite(&ts, mut)

	// Marshal JSON fields.
	deps, wf, rr, bud, ls, err := marshalJSON(ts)
	if err != nil {
		return err
	}

	res, err := ex.Exec(
		`UPDATE tstate_tasks SET
		  status=?, priority=?, attempt=?, spawn_depth=?,
		  input_ref=?, result_ref=?, staged_result_ref=?, checkpoint_ref=?,
		  last_error=?, failed_reason=?,
		  lease_worker=?, lease_expires=?,
		  started_at=?, finished_at=?,
		  deps=?, waiting_for=?, resource_req=?, budget=?, leaf_spec=?
		 WHERE id=? AND status=?`,
		string(ts.Status),
		ts.Priority,
		ts.Attempt,
		ts.SpawnDepth,
		string(ts.InputRef),
		string(ts.ResultRef),
		string(ts.StagedResultRef),
		string(ts.CheckpointRef),
		ts.LastError,
		string(ts.FailedReason),
		ts.Lease.WorkerID,
		ts.Lease.ExpiresAt.UnixNano(),
		ts.StartedAt.UnixNano(),
		ts.FinishedAt.UnixNano(),
		deps, wf, rr, bud, ls,
		string(id),
		string(expect),
	)
	if err != nil {
		return fmt.Errorf("tstate/store/sqlite CAS update %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("tstate/store/sqlite CAS %s: %w", id, ErrCASConflict)
	}
	return nil
}

func applyMutationSQLite(ts *tstate.TaskState, mut tstate.Mutation) {
	if mut.Lease != nil {
		ts.Lease = *mut.Lease
	}
	if mut.Attempt != nil {
		ts.Attempt = *mut.Attempt
	}
	if mut.ResultRef != nil {
		ts.ResultRef = *mut.ResultRef
	}
	if mut.StagedResultRef != nil {
		ts.StagedResultRef = *mut.StagedResultRef
	}
	if mut.WaitingFor != nil {
		ts.WaitingFor = append(ts.WaitingFor[:0], mut.WaitingFor...)
	}
	if mut.LastError != nil {
		ts.LastError = *mut.LastError
	}
	if mut.FailedReason != nil {
		ts.FailedReason = *mut.FailedReason
	}
}

// --- selectCols and scan helpers ---

const selectCols = `id, team_id, session_id, parent_id, kind, status,
  priority, attempt, spawn_depth,
  input_ref, result_ref, staged_result_ref, checkpoint_ref,
  last_error, failed_reason,
  lease_worker, lease_expires,
  created_at, started_at, finished_at,
  deps, waiting_for, resource_req, budget, leaf_spec`

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(row scanner) (tstate.TaskState, error) {
	var (
		id, teamID, sessionID, parentID string
		kind, status                    string
		priority, attempt, spawnDepth   int64
		inputRef, resultRef             string
		stagedResultRef, checkpointRef  string
		lastError, failedReason         string
		leaseWorker                     string
		leaseExpires                    int64
		createdAt, startedAt, finishedAt int64
		depsJSON, wfJSON                string
		rrJSON, budJSON, lsJSON         string
	)
	err := row.Scan(
		&id, &teamID, &sessionID, &parentID,
		&kind, &status,
		&priority, &attempt, &spawnDepth,
		&inputRef, &resultRef, &stagedResultRef, &checkpointRef,
		&lastError, &failedReason,
		&leaseWorker, &leaseExpires,
		&createdAt, &startedAt, &finishedAt,
		&depsJSON, &wfJSON, &rrJSON, &budJSON, &lsJSON,
	)
	if err != nil {
		return tstate.TaskState{}, err
	}

	var deps []types.TaskID
	var wf []types.TaskID
	var rr types.ResourceReq
	var bud types.Budget
	var ls spec.TaskSpec

	if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil {
		deps = nil
	}
	if err := json.Unmarshal([]byte(wfJSON), &wf); err != nil {
		wf = nil
	}
	_ = json.Unmarshal([]byte(rrJSON), &rr)
	_ = json.Unmarshal([]byte(budJSON), &bud)
	_ = json.Unmarshal([]byte(lsJSON), &ls)

	ts := tstate.TaskState{
		ID:              types.TaskID(id),
		TeamID:          types.TeamID(teamID),
		SessionID:       sessionID,
		ParentID:        types.TaskID(parentID),
		Kind:            types.Kind(kind),
		Status:          types.Status(status),
		Priority:        int8(priority),
		Attempt:         int(attempt),
		SpawnDepth:      int(spawnDepth),
		InputRef:        types.Ref(inputRef),
		ResultRef:       types.Ref(resultRef),
		StagedResultRef: types.Ref(stagedResultRef),
		CheckpointRef:   types.Ref(checkpointRef),
		LastError:       lastError,
		FailedReason:    types.FailureReason(failedReason),
		Lease: types.Lease{
			WorkerID:  leaseWorker,
			ExpiresAt: unixNano(leaseExpires),
		},
		CreatedAt:   unixNano(createdAt),
		StartedAt:   unixNano(startedAt),
		FinishedAt:  unixNano(finishedAt),
		Deps:        deps,
		WaitingFor:  wf,
		ResourceReq: rr,
		Budget:      bud,
		LeafSpec:    ls,
	}
	return ts, nil
}

func scanRows(rows *sql.Rows) ([]tstate.TaskState, error) {
	var out []tstate.TaskState
	for rows.Next() {
		ts, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// --- marshal / unmarshal helpers ---

func marshalJSON(ts tstate.TaskState) (deps, wf, rr, bud, ls string, err error) {
	depsIDs := make([]string, len(ts.Deps))
	for i, d := range ts.Deps {
		depsIDs[i] = string(d)
	}
	wfIDs := make([]string, len(ts.WaitingFor))
	for i, d := range ts.WaitingFor {
		wfIDs[i] = string(d)
	}

	depsB, e := json.Marshal(depsIDs)
	if e != nil {
		return "", "", "", "", "", fmt.Errorf("marshal deps: %w", e)
	}
	wfB, e := json.Marshal(wfIDs)
	if e != nil {
		return "", "", "", "", "", fmt.Errorf("marshal waiting_for: %w", e)
	}
	rrB, e := json.Marshal(ts.ResourceReq)
	if e != nil {
		return "", "", "", "", "", fmt.Errorf("marshal resource_req: %w", e)
	}
	budB, e := json.Marshal(ts.Budget)
	if e != nil {
		return "", "", "", "", "", fmt.Errorf("marshal budget: %w", e)
	}
	lsB, e := json.Marshal(ts.LeafSpec)
	if e != nil {
		return "", "", "", "", "", fmt.Errorf("marshal leaf_spec: %w", e)
	}
	return string(depsB), string(wfB), string(rrB), string(budB), string(lsB), nil
}

// unixNano converts a stored int64 nanosecond timestamp back to time.Time.
// A zero value maps to zero time.
func unixNano(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}
