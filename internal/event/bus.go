// Package event provides a simple in-process event bus for decoupled
// communication between modules.
package event

import (
	"sync"
)

// Topic identifies an event category.
type Topic string

const (
	// Session lifecycle
	TopicSessionCreated    Topic = "session.created"
	TopicSessionArchived   Topic = "session.archived"
	TopicSessionIdle       Topic = "session.idle"
	TopicSessionRestored   Topic = "session.restored"
	TopicSessionTerminated Topic = "session.terminated"

	// Channel connectivity
	TopicChannelConnected    Topic = "channel.connected"
	TopicChannelDisconnected Topic = "channel.disconnected"

	// Query engine
	TopicQueryStarted   Topic = "query.started"
	TopicQueryCompleted Topic = "query.completed"
	TopicToolExecuted   Topic = "tool.executed"

	// Context compaction
	TopicCompactTriggered Topic = "compact.triggered"

	// Permission
	TopicPermissionRequested Topic = "permission.requested"
	TopicPermissionDecided   Topic = "permission.decided"

	// API / Provider
	TopicAPIError         Topic = "api.error"
	TopicAPIRetry         Topic = "api.retry"
	TopicProviderSwitched Topic = "provider.switched"
	TopicProviderFallback Topic = "provider.fallback"

	// Sub-agent lifecycle
	TopicSubAgentStarted Topic = "subagent.started"
	TopicSubAgentEnded   Topic = "subagent.ended"
)

// Event is a message published on the event bus.
type Event struct {
	Topic   Topic
	Payload any
}

// Handler processes an event.
type Handler func(evt Event)

// Bus is an in-process publish/subscribe event bus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[Topic][]Handler
}

// NewBus creates an event bus.
func NewBus() *Bus {
	return &Bus{
		handlers: make(map[Topic][]Handler),
	}
}

// Subscribe registers a handler for a topic.
func (b *Bus) Subscribe(topic Topic, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
}

// Publish sends an event to all handlers subscribed to its topic.
// Handlers are called synchronously in subscription order.
func (b *Bus) Publish(evt Event) {
	b.mu.RLock()
	handlers := b.handlers[evt.Topic]
	b.mu.RUnlock()

	for _, h := range handlers {
		h(evt)
	}
}

// PublishAsync sends an event asynchronously — each handler runs in its own
// goroutine with panic recovery to prevent a single handler crash from
// bringing down the process.
func (b *Bus) PublishAsync(evt Event) {
	b.mu.RLock()
	snapshot := make([]Handler, len(b.handlers[evt.Topic]))
	copy(snapshot, b.handlers[evt.Topic])
	b.mu.RUnlock()

	for _, h := range snapshot {
		h := h
		go func() {
			defer func() {
				recover() // swallow panics in async handlers
			}()
			h(evt)
		}()
	}
}
