// Package channel defines the Channel interface for message ingestion.
//
// Each channel (Feishu, WebSocket, HTTP API) implements this interface
// to receive messages from external sources and deliver responses.
package channel

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// MessageHandler is invoked by a channel when a new message arrives.
type MessageHandler func(ctx context.Context, msg *types.IncomingMessage) error

// Channel represents a message ingestion endpoint.
type Channel interface {
	// Name returns the channel identifier (e.g. "feishu", "websocket", "http").
	Name() string

	// Start begins listening for messages. Received messages are delivered
	// via the handler callback. Start blocks until ctx is cancelled or
	// a fatal error occurs.
	Start(ctx context.Context, handler MessageHandler) error

	// Stop performs a graceful shutdown, draining in-flight messages.
	Stop(ctx context.Context) error

	// Send delivers a complete response message to the specified session.
	Send(ctx context.Context, sessionID string, msg *types.Message) error

	// SendEvent pushes a single streaming engine event to the session.
	// This is the primary mechanism for delivering LLM responses in real time.
	SendEvent(ctx context.Context, sessionID string, event *types.EngineEvent) error

	// Health returns nil when the channel is ready, or an error describing
	// why it is unhealthy.
	Health() error
}
