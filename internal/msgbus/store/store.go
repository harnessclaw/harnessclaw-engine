// internal/msgbus/store/store.go
// Package store is the persistence boundary for msgbus.
// Phase 1: memory implementation; phase 1 末: sqlite (shared *sql.DB).
package store

import (
	"context"
	"time"

	"harnessclaw-go/internal/msgbus"
)

// Store persists envelopes + delivery state.
type Store interface {
	Enqueue(ctx context.Context, msg msgbus.AgentMessage) error
	Dequeue(ctx context.Context, topic, consumerID string) (msgbus.AgentMessage, error)
	Ack(ctx context.Context, msgID string) error
	Nack(ctx context.Context, msgID string, retry bool) error

	// Reaper returns delivered-but-not-acked messages whose delivery timestamp
	// is older than (now - deliveryTTL). Callers should requeue or fail them.
	Reaper(ctx context.Context, now time.Time, deliveryTTL time.Duration) ([]msgbus.AgentMessage, error)

	// Query helpers (back BusQuery)
	GetMessage(ctx context.Context, msgID string) (msgbus.AgentMessage, msgbus.MsgStatus, msgbus.MessageMeta, error)
	ListByStatus(ctx context.Context, status msgbus.MsgStatus, kind msgbus.Kind, limit int) ([]msgbus.AgentMessage, error)
	ListByTaskID(ctx context.Context, taskID string) ([]msgbus.MessageRecord, error)
	QueueStats(ctx context.Context) ([]msgbus.QueueStat, error)

	Close() error
}
