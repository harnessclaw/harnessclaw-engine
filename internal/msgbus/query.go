// internal/msgbus/query.go
package msgbus

import "time"

// BusQuery is the read-only inspection surface. NOT to be used for scheduling decisions
// (which must go via tstate). For L1 debugging and monitoring only.
type BusQuery interface {
	GetMessage(msgID string) (AgentMessage, MsgStatus, MessageMeta, error)
	ListByStatus(status MsgStatus, kind Kind, limit int) ([]AgentMessage, error)
	ListByTaskID(taskID string) ([]MessageRecord, error)
	QueueStats() []QueueStat
}

// MessageMeta carries delivery metadata.
type MessageMeta struct {
	DeliveredTo string
	DeliveredAt time.Time
	AckedAt     time.Time
	RetryCount  int
}

// MessageRecord is the join of envelope + meta + status.
type MessageRecord struct {
	Msg         AgentMessage
	Status      MsgStatus
	DeliveredTo string
	DeliveredAt time.Time
	AckedAt     time.Time
	RetryCount  int
}

// QueueStat summarises one queue.
type QueueStat struct {
	Topic         string
	Queued        int
	Delivered     int
	AckedLast24h  int
	FailedLast24h int
	OldestQueued  time.Time
}
