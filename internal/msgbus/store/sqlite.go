// internal/msgbus/store/sqlite.go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"harnessclaw-go/internal/msgbus"
)

const ddl = `
CREATE TABLE IF NOT EXISTS msgbus_messages (
    msg_id        TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    topic         TEXT,
    task_id       TEXT,
    session_id    TEXT,
    payload       BLOB,
    status        TEXT NOT NULL,
    delivered_to  TEXT,
    delivered_at  TIMESTAMP,
    acked_at      TIMESTAMP,
    retry_count   INTEGER DEFAULT 0,
    created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_msgbus_status ON msgbus_messages(status, kind, topic);
CREATE INDEX IF NOT EXISTS idx_msgbus_task   ON msgbus_messages(task_id);
CREATE INDEX IF NOT EXISTS idx_msgbus_session ON msgbus_messages(session_id);
`

// SQLite is a durable Store backed by a shared *sql.DB.
// The caller owns the *sql.DB lifetime; Close() is a no-op.
type SQLite struct {
	db *sql.DB
}

// NewSQLite wraps an existing *sql.DB (from internal/storage/sqlite.Store.DB())
// and runs the msgbus schema migration idempotently.
func NewSQLite(db *sql.DB) (*SQLite, error) {
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("msgbus/store/sqlite migrate: %w", err)
	}
	return s, nil
}

func (s *SQLite) migrate() error {
	_, err := s.db.Exec(ddl)
	return err
}

// Close is a no-op: the caller owns *sql.DB.
func (s *SQLite) Close() error { return nil }

// Enqueue inserts the message as JSON blob with status=queued.
func (s *SQLite) Enqueue(_ context.Context, msg msgbus.AgentMessage) error {
	blob, err := json.Marshal(msg.Payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	topic, _ := msg.To.QueueName()
	_, err = s.db.Exec(
		`INSERT INTO msgbus_messages
		 (msg_id, kind, topic, task_id, session_id, payload, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.MsgID,
		string(msg.Kind),
		topic,
		msg.TaskID,
		msg.SessionID,
		blob,
		string(msgbus.MsgQueued),
	)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", msg.MsgID, err)
	}
	return nil
}

// Dequeue blocks until a queued message for topic is available or ctx is cancelled.
// On success it atomically marks the message as delivered to consumerID.
func (s *SQLite) Dequeue(ctx context.Context, topic, consumerID string) (msgbus.AgentMessage, error) {
	for {
		msg, ok, err := s.tryDequeue(ctx, topic, consumerID)
		if err != nil {
			return msgbus.AgentMessage{}, err
		}
		if ok {
			return msg, nil
		}

		select {
		case <-ctx.Done():
			return msgbus.AgentMessage{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// tryDequeue attempts a single dequeue cycle inside a transaction.
// Returns (msg, true, nil) on success; (zero, false, nil) when the queue is empty.
func (s *SQLite) tryDequeue(ctx context.Context, topic, consumerID string) (msgbus.AgentMessage, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return msgbus.AgentMessage{}, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		msgID   string
		kind    string
		taskID  sql.NullString
		sessID  sql.NullString
		blob    []byte
	)
	err = tx.QueryRowContext(ctx,
		`SELECT msg_id, kind, task_id, session_id, payload
		 FROM msgbus_messages
		 WHERE status = ? AND topic = ?
		 ORDER BY created_at ASC
		 LIMIT 1`,
		string(msgbus.MsgQueued), topic,
	).Scan(&msgID, &kind, &taskID, &sessID, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return msgbus.AgentMessage{}, false, nil
	}
	if err != nil {
		return msgbus.AgentMessage{}, false, fmt.Errorf("select queued: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		`UPDATE msgbus_messages
		 SET status = ?, delivered_to = ?, delivered_at = ?
		 WHERE msg_id = ?`,
		string(msgbus.MsgDelivered), consumerID, now, msgID,
	)
	if err != nil {
		return msgbus.AgentMessage{}, false, fmt.Errorf("mark delivered: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return msgbus.AgentMessage{}, false, fmt.Errorf("commit dequeue: %w", err)
	}

	// Reconstruct the envelope from column values + raw payload blob.
	msg := msgbus.AgentMessage{
		MsgID:     msgID,
		Kind:      msgbus.Kind(kind),
		To:        msgbus.AddrQueue(topic),
		TaskID:    taskID.String,
		SessionID: sessID.String,
	}
	// Unmarshal payload as raw JSON — callers can re-assert the concrete type.
	var raw json.RawMessage
	if err := json.Unmarshal(blob, &raw); err == nil {
		msg.Payload = raw
	}
	return msg, true, nil
}

// Ack marks a message as acked.
func (s *SQLite) Ack(_ context.Context, msgID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(
		`UPDATE msgbus_messages SET status = ?, acked_at = ? WHERE msg_id = ?`,
		string(msgbus.MsgAcked), now, msgID,
	)
	if err != nil {
		return fmt.Errorf("ack %s: %w", msgID, err)
	}
	return expectOneRow(res, "ack", msgID)
}

// Nack re-queues (retry=true) or fails (retry=false) a message.
func (s *SQLite) Nack(_ context.Context, msgID string, retry bool) error {
	var err error
	if retry {
		_, err = s.db.Exec(
			`UPDATE msgbus_messages
			 SET status = ?, retry_count = retry_count + 1, delivered_to = NULL, delivered_at = NULL
			 WHERE msg_id = ?`,
			string(msgbus.MsgQueued), msgID,
		)
	} else {
		_, err = s.db.Exec(
			`UPDATE msgbus_messages SET status = ? WHERE msg_id = ?`,
			string(msgbus.MsgFailed), msgID,
		)
	}
	return err
}

// Reaper returns delivered messages whose delivery_at is older than now-deliveryTTL.
func (s *SQLite) Reaper(_ context.Context, now time.Time, deliveryTTL time.Duration) ([]msgbus.AgentMessage, error) {
	cutoff := now.Add(-deliveryTTL).UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(
		`SELECT msg_id, kind, topic, task_id, session_id, payload
		 FROM msgbus_messages
		 WHERE status = ? AND delivered_at < ?`,
		string(msgbus.MsgDelivered), cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("reaper query: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// GetMessage returns the envelope + status + meta for a single message.
func (s *SQLite) GetMessage(_ context.Context, msgID string) (msgbus.AgentMessage, msgbus.MsgStatus, msgbus.MessageMeta, error) {
	var (
		kind        string
		topic       sql.NullString
		taskID      sql.NullString
		sessID      sql.NullString
		blob        []byte
		status      string
		deliveredTo sql.NullString
		deliveredAt sql.NullString
		ackedAt     sql.NullString
		retryCount  int
	)
	err := s.db.QueryRow(
		`SELECT kind, topic, task_id, session_id, payload,
		        status, delivered_to, delivered_at, acked_at, retry_count
		 FROM msgbus_messages WHERE msg_id = ?`, msgID,
	).Scan(&kind, &topic, &taskID, &sessID, &blob,
		&status, &deliveredTo, &deliveredAt, &ackedAt, &retryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return msgbus.AgentMessage{}, "", msgbus.MessageMeta{}, fmt.Errorf("store: unknown msg_id %s", msgID)
	}
	if err != nil {
		return msgbus.AgentMessage{}, "", msgbus.MessageMeta{}, fmt.Errorf("get message: %w", err)
	}

	msg := msgbus.AgentMessage{
		MsgID:     msgID,
		Kind:      msgbus.Kind(kind),
		TaskID:    taskID.String,
		SessionID: sessID.String,
	}
	if topic.Valid {
		msg.To = msgbus.AddrQueue(topic.String)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(blob, &raw); err == nil {
		msg.Payload = raw
	}

	meta := msgbus.MessageMeta{
		DeliveredTo: deliveredTo.String,
		DeliveredAt: parseTime(deliveredAt.String),
		AckedAt:     parseTime(ackedAt.String),
		RetryCount:  retryCount,
	}
	return msg, msgbus.MsgStatus(status), meta, nil
}

// ListByStatus returns messages matching status and optionally kind, up to limit.
func (s *SQLite) ListByStatus(_ context.Context, status msgbus.MsgStatus, kind msgbus.Kind, limit int) ([]msgbus.AgentMessage, error) {
	query := `SELECT msg_id, kind, topic, task_id, session_id, payload
	          FROM msgbus_messages WHERE status = ?`
	args := []any{string(status)}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, string(kind))
	}
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list by status: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListByTaskID returns all records for a given task_id.
func (s *SQLite) ListByTaskID(_ context.Context, taskID string) ([]msgbus.MessageRecord, error) {
	rows, err := s.db.Query(
		`SELECT msg_id, kind, topic, task_id, session_id, payload,
		        status, delivered_to, delivered_at, acked_at, retry_count
		 FROM msgbus_messages WHERE task_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("list by task_id: %w", err)
	}
	defer rows.Close()

	var out []msgbus.MessageRecord
	for rows.Next() {
		var (
			msgID, kind string
			topic, tid  sql.NullString
			sessID      sql.NullString
			blob        []byte
			status      string
			delivTo     sql.NullString
			delivAt     sql.NullString
			ackedAt     sql.NullString
			retryCount  int
		)
		if err := rows.Scan(&msgID, &kind, &topic, &tid, &sessID, &blob,
			&status, &delivTo, &delivAt, &ackedAt, &retryCount); err != nil {
			return nil, err
		}
		msg := msgbus.AgentMessage{
			MsgID: msgID, Kind: msgbus.Kind(kind),
			TaskID: tid.String, SessionID: sessID.String,
		}
		if topic.Valid {
			msg.To = msgbus.AddrQueue(topic.String)
		}
		var raw json.RawMessage
		if err := json.Unmarshal(blob, &raw); err == nil {
			msg.Payload = raw
		}
		out = append(out, msgbus.MessageRecord{
			Msg: msg, Status: msgbus.MsgStatus(status),
			DeliveredTo: delivTo.String,
			DeliveredAt: parseTime(delivAt.String),
			AckedAt:     parseTime(ackedAt.String),
			RetryCount:  retryCount,
		})
	}
	return out, rows.Err()
}

// QueueStats returns per-topic counters.
func (s *SQLite) QueueStats(_ context.Context) ([]msgbus.QueueStat, error) {
	rows, err := s.db.Query(
		`SELECT topic, status, COUNT(*) FROM msgbus_messages
		 WHERE topic IS NOT NULL
		 GROUP BY topic, status`)
	if err != nil {
		return nil, fmt.Errorf("queue stats: %w", err)
	}
	defer rows.Close()

	stats := map[string]*msgbus.QueueStat{}
	for rows.Next() {
		var topic, status string
		var count int
		if err := rows.Scan(&topic, &status, &count); err != nil {
			return nil, err
		}
		s, ok := stats[topic]
		if !ok {
			s = &msgbus.QueueStat{Topic: topic}
			stats[topic] = s
		}
		switch msgbus.MsgStatus(status) {
		case msgbus.MsgQueued:
			s.Queued += count
		case msgbus.MsgDelivered:
			s.Delivered += count
		case msgbus.MsgAcked:
			s.AckedLast24h += count
		case msgbus.MsgFailed:
			s.FailedLast24h += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]msgbus.QueueStat, 0, len(stats))
	for _, v := range stats {
		out = append(out, *v)
	}
	return out, nil
}

// --- helpers ---

func expectOneRow(res sql.Result, op, msgID string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("store: %s: unknown msg_id %s", op, msgID)
	}
	return nil
}

// scanMessages reads rows of (msg_id, kind, topic, task_id, session_id, payload).
func scanMessages(rows *sql.Rows) ([]msgbus.AgentMessage, error) {
	var out []msgbus.AgentMessage
	for rows.Next() {
		var (
			msgID, kind string
			topic       sql.NullString
			taskID      sql.NullString
			sessID      sql.NullString
			blob        []byte
		)
		if err := rows.Scan(&msgID, &kind, &topic, &taskID, &sessID, &blob); err != nil {
			return nil, err
		}
		msg := msgbus.AgentMessage{
			MsgID:     msgID,
			Kind:      msgbus.Kind(kind),
			TaskID:    taskID.String,
			SessionID: sessID.String,
		}
		if topic.Valid {
			msg.To = msgbus.AddrQueue(topic.String)
		}
		var raw json.RawMessage
		if err := json.Unmarshal(blob, &raw); err == nil {
			msg.Payload = raw
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
