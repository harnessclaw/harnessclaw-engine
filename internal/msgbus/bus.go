// internal/msgbus/bus.go
package msgbus

import (
	"context"
	"time"
)

// Bus is the message-passing core. Implementations must be safe for concurrent use.
type Bus interface {
	// Publish enqueues msg. Returns error if the store rejects (e.g. duplicate MsgID).
	Publish(ctx context.Context, msg AgentMessage) error

	// Subscribe returns a channel of messages addressed to `to`.
	// The returned Cancel function MUST be called to release subscription resources.
	Subscribe(to Address) (<-chan AgentMessage, Cancel)

	// SubscribeOnce returns a channel that fires once on a message matching all filters,
	// then auto-cancels. filters may include Kind, taskID, NotifyEvent. Empty filter
	// matches any message to the subscriber's implicit address.
	//
	// IMPORTANT (v3.1-R6): callers MUST Subscribe BEFORE Publishing the request that
	// will produce the awaited message, or they race the producer.
	SubscribeOnce(filters ...any) (<-chan AgentMessage, Cancel)

	// Dequeue pulls the next pending msg for the given topic, marking it delivered.
	// Blocks until a message is available or ctx is cancelled.
	Dequeue(ctx context.Context, topic string, consumerID string) (AgentMessage, error)

	// Ack marks a delivered message as successfully consumed.
	Ack(msgID string) error

	// Nack rejects a delivered message. If retry=true, msg is requeued; else marked failed.
	Nack(msgID string, retry bool) error

	// Query exposes read-only inspection (for L1 debugging / monitoring).
	Query() BusQuery
}

// Cancel releases subscription resources.
type Cancel func()

// SubscribeFilter is a typed filter for SubscribeOnce.
type SubscribeFilter struct {
	Kind   Kind        // empty = any
	TaskID string      // empty = any
	Notify NotifyEvent // empty = any (only meaningful when Kind == KindNotify)
}

// FilterKind builds a Kind-only filter.
func FilterKind(k Kind) SubscribeFilter { return SubscribeFilter{Kind: k} }

// FilterKindTask builds a Kind+TaskID filter.
func FilterKindTask(k Kind, taskID string) SubscribeFilter {
	return SubscribeFilter{Kind: k, TaskID: taskID}
}

// FilterNotify builds a notification-event filter scoped to a task.
func FilterNotify(taskID string, ev NotifyEvent) SubscribeFilter {
	return SubscribeFilter{Kind: KindNotify, TaskID: taskID, Notify: ev}
}

// DefaultDeliveryTTL is the default redelivery timeout for unacked messages.
const DefaultDeliveryTTL = 30 * time.Second
