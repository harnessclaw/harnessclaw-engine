package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MessageBroker routes messages between agents.
type MessageBroker struct {
	mu        sync.RWMutex
	mailboxes map[string]*Mailbox // agentName -> mailbox
	teams     map[string][]string // teamID -> member names
}

// NewMessageBroker creates a new broker.
func NewMessageBroker() *MessageBroker {
	return &MessageBroker{
		mailboxes: make(map[string]*Mailbox),
		teams:     make(map[string][]string),
	}
}

// Register creates a mailbox for an agent and optionally adds it to a team.
func (b *MessageBroker) Register(name, teamID string) *Mailbox {
	b.mu.Lock()
	defer b.mu.Unlock()

	mb := NewMailbox(name, 64)
	b.mailboxes[name] = mb

	if teamID != "" {
		b.teams[teamID] = append(b.teams[teamID], name)
	}
	return mb
}

// Unregister removes an agent's mailbox and team membership.
func (b *MessageBroker) Unregister(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if mb, ok := b.mailboxes[name]; ok {
		mb.Close()
		delete(b.mailboxes, name)
	}

	// Remove from all teams
	for teamID, members := range b.teams {
		for i, m := range members {
			if m == name {
				b.teams[teamID] = append(members[:i], members[i+1:]...)
				break
			}
		}
	}
}

// Send delivers a message to a specific agent. Returns error if recipient not found.
func (b *MessageBroker) Send(msg *AgentMessage) error {
	if msg.ID == "" {
		msg.ID = "msg_" + uuid.New().String()[:8]
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	mb, ok := b.mailboxes[msg.To]
	if !ok {
		return fmt.Errorf("recipient %q not found", msg.To)
	}
	if !mb.Send(msg) {
		return fmt.Errorf("mailbox %q is full or closed", msg.To)
	}
	return nil
}

// Broadcast sends a message to all members of a team except the sender.
func (b *MessageBroker) Broadcast(teamID string, msg *AgentMessage) (int, error) {
	if msg.ID == "" {
		msg.ID = "msg_" + uuid.New().String()[:8]
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}

	b.mu.RLock()
	members := make([]string, len(b.teams[teamID]))
	copy(members, b.teams[teamID])
	b.mu.RUnlock()

	sent := 0
	for _, name := range members {
		if name == msg.From {
			continue
		}
		// Create a copy for each recipient
		cp := *msg
		cp.To = name
		b.mu.RLock()
		mb, ok := b.mailboxes[name]
		b.mu.RUnlock()
		if ok && mb.Send(&cp) {
			sent++
		}
	}
	return sent, nil
}

// TeamMembers returns the list of agent names in a team.
func (b *MessageBroker) TeamMembers(teamID string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	members := b.teams[teamID]
	cp := make([]string, len(members))
	copy(cp, members)
	return cp
}

// HasMailbox checks if an agent name is registered.
func (b *MessageBroker) HasMailbox(name string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.mailboxes[name]
	return ok
}
