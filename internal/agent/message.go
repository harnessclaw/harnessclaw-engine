package agent

import "time"

// MessageType classifies agent-to-agent messages.
type MessageType string

const (
	MessageTypePlain            MessageType = "plain"
	MessageTypeShutdownRequest  MessageType = "shutdown_request"
	MessageTypeShutdownResponse MessageType = "shutdown_response"
)

// AgentMessage represents a message between agents.
type AgentMessage struct {
	ID        string      `json:"id"`
	From      string      `json:"from"`       // sender agent name
	To        string      `json:"to"`         // recipient agent name or "*" for broadcast
	Type      MessageType `json:"type"`
	Content   string      `json:"content"`    // plain text content
	TeamID    string      `json:"team_id,omitempty"`
	CreatedAt time.Time   `json:"created_at"`

	// Structured message fields (for shutdown_request/response)
	Reason    string `json:"reason,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Approved  *bool  `json:"approved,omitempty"`
}
