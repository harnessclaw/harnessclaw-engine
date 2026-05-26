// internal/msgbus/store/memory.go
package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"harnessclaw-go/internal/msgbus"
)

type memRow struct {
	msg         msgbus.AgentMessage
	status      msgbus.MsgStatus
	deliveredTo string
	deliveredAt time.Time
	ackedAt     time.Time
	retryCount  int
}

// Memory is a thread-safe in-process Store. Suitable for tests and single-process dev.
type Memory struct {
	mu     sync.Mutex
	byID   map[string]*memRow
	queues map[string][]string // topic → ordered msgID list (queued only)
	notify chan struct{}
}

// NewMemory constructs an in-memory Store.
func NewMemory() *Memory {
	return &Memory{
		byID:   map[string]*memRow{},
		queues: map[string][]string{},
		notify: make(chan struct{}, 1),
	}
}

func (m *Memory) Close() error { return nil }

func (m *Memory) Enqueue(_ context.Context, msg msgbus.AgentMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byID[msg.MsgID]; exists {
		return errors.New("store: duplicate msg_id " + msg.MsgID)
	}
	m.byID[msg.MsgID] = &memRow{msg: msg, status: msgbus.MsgQueued}
	if topic, ok := msg.To.QueueName(); ok {
		m.queues[topic] = append(m.queues[topic], msg.MsgID)
	}
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

func (m *Memory) Dequeue(ctx context.Context, topic, consumerID string) (msgbus.AgentMessage, error) {
	for {
		m.mu.Lock()
		q := m.queues[topic]
		if len(q) > 0 {
			msgID := q[0]
			m.queues[topic] = q[1:]
			row := m.byID[msgID]
			row.status = msgbus.MsgDelivered
			row.deliveredTo = consumerID
			row.deliveredAt = time.Now()
			msg := row.msg
			m.mu.Unlock()
			return msg, nil
		}
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return msgbus.AgentMessage{}, ctx.Err()
		case <-m.notify:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *Memory) Ack(_ context.Context, msgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.byID[msgID]
	if !ok {
		return errors.New("store: unknown msg_id " + msgID)
	}
	row.status = msgbus.MsgAcked
	row.ackedAt = time.Now()
	return nil
}

func (m *Memory) Nack(_ context.Context, msgID string, retry bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.byID[msgID]
	if !ok {
		return errors.New("store: unknown msg_id " + msgID)
	}
	if retry {
		row.status = msgbus.MsgQueued
		row.retryCount++
		if topic, ok := row.msg.To.QueueName(); ok {
			m.queues[topic] = append(m.queues[topic], msgID)
		}
		select {
		case m.notify <- struct{}{}:
		default:
		}
	} else {
		row.status = msgbus.MsgFailed
	}
	return nil
}

func (m *Memory) Reaper(_ context.Context, now time.Time, ttl time.Duration) ([]msgbus.AgentMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var expired []msgbus.AgentMessage
	for _, row := range m.byID {
		if row.status == msgbus.MsgDelivered && now.Sub(row.deliveredAt) > ttl {
			expired = append(expired, row.msg)
		}
	}
	return expired, nil
}

func (m *Memory) GetMessage(_ context.Context, msgID string) (msgbus.AgentMessage, msgbus.MsgStatus, msgbus.MessageMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.byID[msgID]
	if !ok {
		return msgbus.AgentMessage{}, "", msgbus.MessageMeta{}, errors.New("store: unknown msg_id " + msgID)
	}
	return row.msg, row.status, msgbus.MessageMeta{
		DeliveredTo: row.deliveredTo, DeliveredAt: row.deliveredAt,
		AckedAt: row.ackedAt, RetryCount: row.retryCount,
	}, nil
}

func (m *Memory) ListByStatus(_ context.Context, st msgbus.MsgStatus, kind msgbus.Kind, limit int) ([]msgbus.AgentMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []msgbus.AgentMessage
	for _, row := range m.byID {
		if row.status == st && (kind == "" || row.msg.Kind == kind) {
			out = append(out, row.msg)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *Memory) ListByTaskID(_ context.Context, taskID string) ([]msgbus.MessageRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []msgbus.MessageRecord
	for _, row := range m.byID {
		if row.msg.TaskID == taskID {
			out = append(out, msgbus.MessageRecord{
				Msg: row.msg, Status: row.status,
				DeliveredTo: row.deliveredTo, DeliveredAt: row.deliveredAt,
				AckedAt: row.ackedAt, RetryCount: row.retryCount,
			})
		}
	}
	return out, nil
}

func (m *Memory) QueueStats(_ context.Context) ([]msgbus.QueueStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := map[string]*msgbus.QueueStat{}
	for _, row := range m.byID {
		topic, _ := row.msg.To.QueueName()
		if topic == "" {
			continue
		}
		s, ok := stats[topic]
		if !ok {
			s = &msgbus.QueueStat{Topic: topic}
			stats[topic] = s
		}
		switch row.status {
		case msgbus.MsgQueued:
			s.Queued++
		case msgbus.MsgDelivered:
			s.Delivered++
		case msgbus.MsgAcked:
			s.AckedLast24h++
		case msgbus.MsgFailed:
			s.FailedLast24h++
		}
	}
	out := make([]msgbus.QueueStat, 0, len(stats))
	for _, s := range stats {
		out = append(out, *s)
	}
	return out, nil
}
