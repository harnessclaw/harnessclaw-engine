package websocket

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

	// pingTimeout is the deadline for a ping/pong round-trip.
	// Must be longer than writeTimeout to tolerate slow clients during streaming.
	pingTimeout = 30 * time.Second
)

// Conn manages a single WebSocket connection: a read pump that receives
// client messages and a write pump that sends server messages.
//
// A connection starts uninitialised — the client must send `session.create`
// before any other message type is accepted. Until then only `session.create`
// and `ping` are processed; all other messages are rejected with an error frame.
type Conn struct {
	id        string
	sessionID string
	userID    string
	ws        *websocket.Conn
	send      chan []byte
	done      chan struct{}
	logger    *zap.Logger
	closeOnce sync.Once

	// initialized is set to true after the client sends session.create
	// and the server responds with session.created.
	initialized bool
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
//
// The connection must be initialised via `session.create` before any other
// message type is processed. Pre-init, only `session.create` and `ping` are
// accepted; other types receive an error frame and are discarded.
func (c *Conn) readPump(ctx context.Context, handler channel.MessageHandler, abortFn func(context.Context, string) error, registry *ConnRegistry, clientTools bool) {
	defer func() {
		// Only unregister if we were registered (i.e. initialized).
		if c.initialized {
			registry.Unregister(c.sessionID, c.id)
		}
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

		// --- Pre-init gate: only session.create and ping are allowed ---
		if !c.initialized {
			switch msg.Type {
			case MsgTypeSessionCreate:
				c.handleSessionCreate(msg, registry, clientTools)
				continue
			case MsgTypePing:
				// Allow ping even before init.
			default:
				c.sendError("session_not_initialized",
					"session not initialized: send session.create first")
				continue
			}
		}

		switch msg.Type {
		case MsgTypeSessionCreate:
			// Already initialized — send error.
			c.sendError("session_already_created",
				"session already created, cannot re-initialize")

		case MsgTypeUserMessage:
			incoming := &types.IncomingMessage{
				ChannelName: "websocket",
				SessionID:   c.sessionID,
				UserID:      c.userID,
			}

			blocks, err := msg.ContentBlocks()
			if err != nil {
				c.logger.Warn("invalid user.message content", zap.Error(err))
				c.sendError("invalid_content", err.Error())
				continue
			}

			if len(blocks) > 0 {
				incoming.Content = toIncomingContentBlocks(blocks)
				// Collect text from all text blocks for backward compat.
				var textParts []string
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				}
				incoming.Text = strings.Join(textParts, "\n")
			} else {
				// Fall back to the shorthand Text field.
				incoming.Text = msg.Text
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

		case MsgTypePermissionResponse:
			approved := false
			if msg.Approved != nil {
				approved = *msg.Approved
			}
			scope := types.PermissionScopeOnce
			if msg.Scope == "session" {
				scope = types.PermissionScopeSession
			}
			incoming := &types.IncomingMessage{
				ChannelName: "websocket",
				SessionID:   c.sessionID,
				UserID:      c.userID,
				PermissionResponse: &types.PermissionResponse{
					RequestID: msg.RequestID,
					Approved:  approved,
					Scope:     scope,
					Message:   msg.Message,
				},
			}
			if err := handler(ctx, incoming); err != nil {
				c.logger.Error("handler error (permission.response)", zap.Error(err))
			}

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

// handleSessionCreate processes the session.create message, binds the session
// to this connection, registers it in the ConnRegistry, and sends session.created.
func (c *Conn) handleSessionCreate(msg ClientMessage, registry *ConnRegistry, clientTools bool) {
	sessionID := msg.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	userID := msg.UserID

	c.sessionID = sessionID
	c.userID = userID
	c.initialized = true
	c.logger = c.logger.With(zap.String("session", sessionID))

	registry.Register(c)

	initMsg := SessionCreatedMessage{
		Type:            MsgTypeSessionCreated,
		EventID:         "evt_" + uuid.New().String()[:8],
		SessionID:       sessionID,
		ProtocolVersion: "1.5",
		Session: SessionInfo{
			Capabilities: Capabilities{
				Streaming:   true,
				Tools:       true,
				ClientTools: clientTools,
				MultiTurn:   true,
			},
		},
	}
	if data, err := json.Marshal(initMsg); err == nil {
		c.TrySend(data)
	}

	c.logger.Info("session created",
		zap.String("session_id", sessionID),
		zap.String("user_id", userID),
	)
}

// sendError sends a structured error frame to the client.
func (c *Conn) sendError(code, message string) {
	errMsg := ErrorMessage{
		Type:      MsgTypeError,
		EventID:   "evt_err",
		SessionID: c.sessionID,
		Error: ErrorDetail{
			Type:    "protocol_error",
			Code:    code,
			Message: message,
		},
	}
	if data, err := json.Marshal(errMsg); err == nil {
		c.TrySend(data)
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
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				// Ping failure during active streaming is expected — the client
				// may be busy processing data. Log but don't kill the connection.
				c.logger.Debug("ping failed (non-fatal during streaming)", zap.Error(err))
			}
		}
	}
}

// toIncomingContentBlocks converts wire-format ClientContentBlock slice to
// the channel-agnostic IncomingContentBlock slice used by the engine.
func toIncomingContentBlocks(blocks []ClientContentBlock) []types.IncomingContentBlock {
	out := make([]types.IncomingContentBlock, 0, len(blocks))
	for _, b := range blocks {
		icb := types.IncomingContentBlock{
			Type: b.Type,
			Text: b.Text,
		}
		if b.Source != nil {
			icb.MIMEType = b.Source.MediaType
			icb.Path = b.Source.Path
			icb.URL = b.Source.URL
			icb.Data = b.Source.Data
		}
		out = append(out, icb)
	}
	return out
}
