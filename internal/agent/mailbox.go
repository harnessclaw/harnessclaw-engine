package agent

import "sync"

// Mailbox is a per-agent message queue.
type Mailbox struct {
	mu       sync.Mutex
	name     string
	messages chan *AgentMessage
	closed   bool
}

// NewMailbox creates a mailbox with the given buffer size.
func NewMailbox(name string, bufSize int) *Mailbox {
	if bufSize <= 0 {
		bufSize = 64
	}
	return &Mailbox{
		name:     name,
		messages: make(chan *AgentMessage, bufSize),
	}
}

// Send delivers a message to this mailbox. Returns false if mailbox is closed or full.
func (mb *Mailbox) Send(msg *AgentMessage) bool {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if mb.closed {
		return false
	}
	select {
	case mb.messages <- msg:
		return true
	default:
		return false // full
	}
}

// Receive returns the receive-only channel for draining messages.
func (mb *Mailbox) Receive() <-chan *AgentMessage {
	return mb.messages
}

// Close closes the mailbox. No more messages can be sent.
func (mb *Mailbox) Close() {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if !mb.closed {
		mb.closed = true
		close(mb.messages)
	}
}

// Name returns the mailbox owner name.
func (mb *Mailbox) Name() string {
	return mb.name
}
