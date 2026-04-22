package sendmessage

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
)

func TestSendMessage_PlainMessage(t *testing.T) {
	broker := agent.NewMessageBroker()
	broker.Register("sender", "team-1")
	mb := broker.Register("receiver", "team-1")

	tool := New(broker, "sender", "team-1", zap.NewNop())

	input := json.RawMessage(`{"to":"receiver","message":"hello world"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if result.Content != "Message sent to receiver" {
		t.Errorf("unexpected result content: %q", result.Content)
	}

	// Verify message was delivered.
	select {
	case msg := <-mb.Receive():
		if msg.Content != "hello world" {
			t.Errorf("expected content 'hello world', got %q", msg.Content)
		}
		if msg.From != "sender" {
			t.Errorf("expected from 'sender', got %q", msg.From)
		}
		if msg.Type != agent.MessageTypePlain {
			t.Errorf("expected type 'plain', got %q", msg.Type)
		}
	default:
		t.Fatal("expected a message in the mailbox")
	}
}

func TestSendMessage_UnknownRecipient(t *testing.T) {
	broker := agent.NewMessageBroker()
	broker.Register("sender", "team-1")

	tool := New(broker, "sender", "team-1", zap.NewNop())

	input := json.RawMessage(`{"to":"nobody","message":"hello"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for unknown recipient")
	}
}

func TestSendMessage_Broadcast(t *testing.T) {
	broker := agent.NewMessageBroker()
	broker.Register("alice", "team-a")
	mbBob := broker.Register("bob", "team-a")
	mbCarol := broker.Register("carol", "team-a")

	tool := New(broker, "alice", "team-a", zap.NewNop())

	input := json.RawMessage(`{"to":"*","message":"broadcast msg"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}
	if result.Content != "Message broadcast to 2 recipients" {
		t.Errorf("unexpected result: %q", result.Content)
	}

	// Verify bob received it.
	select {
	case msg := <-mbBob.Receive():
		if msg.Content != "broadcast msg" {
			t.Errorf("bob: expected 'broadcast msg', got %q", msg.Content)
		}
	default:
		t.Fatal("bob: expected a message")
	}

	// Verify carol received it.
	select {
	case msg := <-mbCarol.Receive():
		if msg.Content != "broadcast msg" {
			t.Errorf("carol: expected 'broadcast msg', got %q", msg.Content)
		}
	default:
		t.Fatal("carol: expected a message")
	}
}

func TestSendMessage_ValidateInput_MissingTo(t *testing.T) {
	broker := agent.NewMessageBroker()
	tool := New(broker, "sender", "team-1", zap.NewNop())

	input := json.RawMessage(`{"message":"hello"}`)
	err := tool.ValidateInput(input)
	if err == nil {
		t.Fatal("expected validation error for missing 'to'")
	}
}

func TestSendMessage_ValidateInput_MissingMessage(t *testing.T) {
	broker := agent.NewMessageBroker()
	tool := New(broker, "sender", "team-1", zap.NewNop())

	input := json.RawMessage(`{"to":"receiver"}`)
	err := tool.ValidateInput(input)
	if err == nil {
		t.Fatal("expected validation error for missing 'message'")
	}
}

func TestSendMessage_ShutdownRequestType(t *testing.T) {
	broker := agent.NewMessageBroker()
	broker.Register("coordinator", "team-1")
	mb := broker.Register("worker", "team-1")

	tool := New(broker, "coordinator", "team-1", zap.NewNop())

	input := json.RawMessage(`{"to":"worker","message":{"type":"shutdown_request","reason":"task complete","request_id":"req-123"}}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Verify the message type was detected.
	select {
	case msg := <-mb.Receive():
		if msg.Type != agent.MessageTypeShutdownRequest {
			t.Errorf("expected type 'shutdown_request', got %q", msg.Type)
		}
	default:
		t.Fatal("expected a message in the mailbox")
	}
}

func TestSendMessage_ShutdownResponseType(t *testing.T) {
	broker := agent.NewMessageBroker()
	broker.Register("worker", "team-1")
	mb := broker.Register("coordinator", "team-1")

	tool := New(broker, "worker", "team-1", zap.NewNop())

	approved := true
	msg := struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Approved  *bool  `json:"approved"`
	}{
		Type:      "shutdown_response",
		RequestID: "req-123",
		Approved:  &approved,
	}
	msgBytes, _ := json.Marshal(msg)
	input, _ := json.Marshal(map[string]any{
		"to":      "coordinator",
		"message": json.RawMessage(msgBytes),
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content)
	}

	// Verify the message type was detected.
	select {
	case received := <-mb.Receive():
		if received.Type != agent.MessageTypeShutdownResponse {
			t.Errorf("expected type 'shutdown_response', got %q", received.Type)
		}
	default:
		t.Fatal("expected a message in the mailbox")
	}
}

func TestSendMessage_Name(t *testing.T) {
	broker := agent.NewMessageBroker()
	tool := New(broker, "test", "", zap.NewNop())
	if tool.Name() != "SendMessage" {
		t.Errorf("expected name 'SendMessage', got %q", tool.Name())
	}
}

func TestSendMessage_IsConcurrencySafe(t *testing.T) {
	broker := agent.NewMessageBroker()
	tool := New(broker, "test", "", zap.NewNop())
	if !tool.IsConcurrencySafe() {
		t.Error("expected IsConcurrencySafe to return true")
	}
}
