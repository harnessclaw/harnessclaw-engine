package websocket

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/pkg/types"
)

const (
	// sendBufSize is the capacity of the per-connection write buffer.
	sendBufSize = 256

	// pingInterval is how often the write pump sends a WebSocket ping.
	pingInterval = 30 * time.Second

	// writeTimeout is the deadline for a single WebSocket write.
	writeTimeout = 10 * time.Second
)

// Conn manages a single WebSocket connection: a read pump that receives
// client messages and a write pump that sends server messages.
type Conn struct {
	id        string
	sessionID string
	userID    string
	ws        *websocket.Conn
	send      chan []byte
	done      chan struct{}
	logger    *zap.Logger
	closeOnce sync.Once
}

// newConn wraps a raw WebSocket connection.
func newConn(id, sessionID, userID string, ws *websocket.Conn, logger *zap.Logger) *Conn {
	return &Conn{
		id:        id,
		sessionID: sessionID,
		userID:    userID,
		ws:        ws,
		send:      make(chan []byte, sendBufSize),
		done:      make(chan struct{}),
		logger:    logger.With(zap.String("conn", id), zap.String("session", sessionID)),
	}
}

// TrySend attempts a non-blocking enqueue. Returns false if the buffer is full
// (the message is dropped rather than blocking the engine goroutine).
func (c *Conn) TrySend(data []byte) bool {
	select {
	case c.send <- data:
		return true
	default:
		c.logger.Warn("write buffer full, dropping message")
		return false
	}
}

// Close performs a one-time graceful close.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close(websocket.StatusNormalClosure, "server shutdown")
	})
}

// readPump reads messages from the client and dispatches them. It runs until
// the connection is closed or ctx is cancelled.
func (c *Conn) readPump(ctx context.Context, handler channel.MessageHandler, abortFn func(context.Context, string) error, registry *ConnRegistry) {
	defer func() {
		registry.Unregister(c.sessionID, c.id)
		c.Close()
	}()

	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			// Normal closure or context cancel — not an error worth logging at error level.
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				c.logger.Debug("client closed connection")
			} else {
				select {
				case <-ctx.Done():
					c.logger.Debug("read pump stopped (context cancelled)")
				default:
					c.logger.Warn("read error", zap.Error(err))
				}
			}
			return
		}

		var msg ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logger.Warn("invalid client message", zap.Error(err))
			continue
		}

		switch msg.Type {
		case MsgTypeUserMessage:
			text := msg.Text
			if msg.Content != nil {
				text = msg.Content.Text
			}
			incoming := &types.IncomingMessage{
				ChannelName: "websocket",
				SessionID:   c.sessionID,
				UserID:      c.userID,
				Text:        text,
			}
			if err := handler(ctx, incoming); err != nil {
				c.logger.Error("handler error", zap.Error(err))
			}

		case MsgTypeToolResult:
			incoming := &types.IncomingMessage{
				ChannelName: "websocket",
				SessionID:   c.sessionID,
				UserID:      c.userID,
				ToolResult: &types.ToolResultPayload{
					ToolUseID: msg.ToolUseID,
					Status:    msg.Status,
					Output:    msg.Output,
				},
			}
			if msg.Error != nil {
				incoming.ToolResult.ErrorCode = msg.Error.Code
				incoming.ToolResult.ErrorMessage = msg.Error.Message
			}
			if err := handler(ctx, incoming); err != nil {
				c.logger.Error("handler error (tool.result)", zap.Error(err))
			}

		case MsgTypeToolProgress:
			c.logger.Debug("tool.progress received",
				zap.String("tool_use_id", msg.ToolUseID),
				zap.String("output", msg.Output),
			)
			// TODO: reset tool timeout timer

		case MsgTypeSessionInterrupt:
			if abortFn != nil {
				if err := abortFn(ctx, c.sessionID); err != nil {
					c.logger.Error("abort failed", zap.Error(err))
				}
			}

		case MsgTypeSessionUpdate:
			c.logger.Debug("session.update received (not yet implemented)")

		case MsgTypePing:
			// Send pong response.
			pong := struct {
				Type      WSMessageType `json:"type"`
				EventID   string        `json:"event_id"`
				SessionID string        `json:"session_id"`
			}{
				Type:      MsgTypePong,
				EventID:   "evt_pong",
				SessionID: c.sessionID,
			}
			if data, err := json.Marshal(pong); err == nil {
				c.TrySend(data)
			}

		default:
			c.logger.Debug("unknown client message type", zap.String("type", string(msg.Type)))
		}
	}
}

// writePump drains the send buffer and writes to the WebSocket. It also sends
// periodic pings to keep the connection alive.
func (c *Conn) writePump(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case data := <-c.send:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				c.logger.Warn("write error", zap.Error(err))
				return
			}
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.logger.Warn("ping failed", zap.Error(err))
				return
			}
		}
	}
}
