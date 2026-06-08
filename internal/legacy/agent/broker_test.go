package agent

import (
	"testing"
)

func TestRegister_CreatesMailbox(t *testing.T) {
	broker := NewMessageBroker()
	mb := broker.Register("agent-a", "team-1")

	if mb == nil {
		t.Fatal("expected non-nil mailbox")
	}
	if mb.Name() != "agent-a" {
		t.Errorf("expected mailbox name 'agent-a', got %q", mb.Name())
	}
	if !broker.HasMailbox("agent-a") {
		t.Error("expected HasMailbox to return true after Register")
	}
}

func TestSend_DeliversToRecipient(t *testing.T) {
	broker := NewMessageBroker()
	mb := broker.Register("agent-b", "")

	msg := &AgentMessage{
		From:    "agent-a",
		To:      "agent-b",
		Type:    MessageTypePlain,
		Content: "hello",
	}

	if err := broker.Send(msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Message ID should be auto-assigned.
	if msg.ID == "" {
		t.Error("expected message ID to be auto-assigned")
	}
	if msg.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be auto-set")
	}

	// Drain the mailbox channel.
	select {
	case received := <-mb.Receive():
		if received.Content != "hello" {
			t.Errorf("expected content 'hello', got %q", received.Content)
		}
		if received.From != "agent-a" {
			t.Errorf("expected from 'agent-a', got %q", received.From)
		}
	default:
		t.Fatal("expected a message in the mailbox")
	}
}

func TestSend_UnknownRecipient_ReturnsError(t *testing.T) {
	broker := NewMessageBroker()

	msg := &AgentMessage{
		From:    "agent-a",
		To:      "nonexistent",
		Type:    MessageTypePlain,
		Content: "hello",
	}

	err := broker.Send(msg)
	if err == nil {
		t.Fatal("expected error for unknown recipient")
	}
}

func TestBroadcast_SendsToTeamExceptSender(t *testing.T) {
	broker := NewMessageBroker()
	broker.Register("alice", "team-x")
	mbBob := broker.Register("bob", "team-x")
	mbCarol := broker.Register("carol", "team-x")

	msg := &AgentMessage{
		From:    "alice",
		Type:    MessageTypePlain,
		Content: "team update",
	}

	sent, err := broker.Broadcast("team-x", msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent != 2 {
		t.Errorf("expected 2 recipients, got %d", sent)
	}

	// Verify bob and carol received the message.
	select {
	case m := <-mbBob.Receive():
		if m.Content != "team update" {
			t.Errorf("bob: expected 'team update', got %q", m.Content)
		}
		if m.To != "bob" {
			t.Errorf("bob: expected To='bob', got %q", m.To)
		}
	default:
		t.Fatal("bob: expected a message")
	}

	select {
	case m := <-mbCarol.Receive():
		if m.Content != "team update" {
			t.Errorf("carol: expected 'team update', got %q", m.Content)
		}
		if m.To != "carol" {
			t.Errorf("carol: expected To='carol', got %q", m.To)
		}
	default:
		t.Fatal("carol: expected a message")
	}
}

func TestUnregister_ClosesMailboxAndRemovesFromTeam(t *testing.T) {
	broker := NewMessageBroker()
	mb := broker.Register("agent-c", "team-y")

	broker.Unregister("agent-c")

	if broker.HasMailbox("agent-c") {
		t.Error("expected HasMailbox to return false after Unregister")
	}

	// Mailbox should be closed — Send should return false.
	ok := mb.Send(&AgentMessage{Content: "after close"})
	if ok {
		t.Error("expected Send to return false on closed mailbox")
	}

	// Team membership should be removed.
	members := broker.TeamMembers("team-y")
	for _, m := range members {
		if m == "agent-c" {
			t.Error("expected agent-c to be removed from team-y")
		}
	}
}

func TestHasMailbox_ReturnsFalseForUnknown(t *testing.T) {
	broker := NewMessageBroker()

	if broker.HasMailbox("nobody") {
		t.Error("expected HasMailbox to return false for unregistered agent")
	}
}

func TestSend_FullMailbox_ReturnsError(t *testing.T) {
	broker := NewMessageBroker()
	// Register with default buffer size (64), then fill it.
	broker.Register("agent-d", "")

	// Fill the mailbox.
	for i := 0; i < 64; i++ {
		msg := &AgentMessage{
			From:    "sender",
			To:      "agent-d",
			Type:    MessageTypePlain,
			Content: "fill",
		}
		if err := broker.Send(msg); err != nil {
			t.Fatalf("unexpected error on message %d: %v", i, err)
		}
	}

	// Next send should fail because the mailbox is full.
	msg := &AgentMessage{
		From:    "sender",
		To:      "agent-d",
		Type:    MessageTypePlain,
		Content: "overflow",
	}
	err := broker.Send(msg)
	if err == nil {
		t.Fatal("expected error when mailbox is full")
	}
}

func TestTeamMembers_ReturnsCorrectList(t *testing.T) {
	broker := NewMessageBroker()
	broker.Register("a1", "team-z")
	broker.Register("a2", "team-z")
	broker.Register("a3", "team-other")

	members := broker.TeamMembers("team-z")
	if len(members) != 2 {
		t.Fatalf("expected 2 members in team-z, got %d", len(members))
	}

	// Verify it's a copy — mutating it should not affect the broker.
	members[0] = "mutated"
	original := broker.TeamMembers("team-z")
	if original[0] == "mutated" {
		t.Error("TeamMembers should return a copy, not the internal slice")
	}
}
