package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

// Channel is the WebSocket implementation of channel.Channel.
// It upgrades HTTP connections to WebSocket and streams engine events
// to all connections subscribed to a given session.
type Channel struct {
	name     string
	config   config.WSChannelConfig
	server   *http.Server
	mux      *http.ServeMux
	registry *ConnRegistry
	handler  channel.MessageHandler
	abortFn  func(ctx context.Context, sessionID string) error
	mappers  sync.Map // sessionID → *EventMapper
	logger   *zap.Logger
	healthy  atomic.Bool

	// connCtx is the context used for all WebSocket connections.
	// It is set in Start() and cancelled in Stop().
	connCtx    context.Context
	connCancel context.CancelFunc
}

// Compile-time check.
var _ channel.Channel = (*Channel)(nil)

// New creates a WebSocket channel. It listens on the host:port from its own config.
func New(
	cfg config.WSChannelConfig,
	abortFn func(context.Context, string) error,
	logger *zap.Logger,
) *Channel {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mux := http.NewServeMux()
	ch := &Channel{
		name:     "websocket",
		config:   cfg,
		mux:      mux,
		registry: NewConnRegistry(),
		abortFn:  abortFn,
		logger:   logger.Named("ws"),
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
	return ch
}

// Name returns "websocket".
func (ch *Channel) Name() string { return ch.name }

// Start registers the upgrade handler, starts the HTTP server, and blocks
// until ctx is cancelled.
func (ch *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	ch.handler = handler
	ch.connCtx, ch.connCancel = context.WithCancel(ctx)

	path := ch.config.Path
	if path == "" {
		path = "/ws"
	}
	ch.mux.HandleFunc(path, ch.upgradeHandler)
	ch.healthy.Store(true)
	ch.logger.Info("websocket channel listening", zap.String("path", path), zap.String("addr", ch.server.Addr))

	// Start the HTTP listener in a goroutine; block on ctx.
	errCh := make(chan error, 1)
	go func() {
		if err := ch.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return ch.Stop(context.Background())
	case err := <-errCh:
		return fmt.Errorf("websocket server error: %w", err)
	}
}

// Stop performs graceful shutdown: close all connections, then shut down the
// HTTP server.
func (ch *Channel) Stop(ctx context.Context) error {
	ch.healthy.Store(false)
	if ch.connCancel != nil {
		ch.connCancel()
	}
	ch.registry.CloseAll()
	return ch.server.Shutdown(ctx)
}

// Send delivers a complete message to all connections for a session.
// This is the non-streaming path for pre-assembled assistant messages.
func (ch *Channel) Send(_ context.Context, sessionID string, msg *types.Message) error {
	items := make([]ContentItem, 0, len(msg.Content))
	for _, cb := range msg.Content {
		items = append(items, ContentItem{
			Type: string(cb.Type),
			Text: cb.Text,
			ID:   cb.ToolUseID,
			Name: cb.ToolName,
		})
	}

	am := AssistantMessage{
		Type:      MsgTypeMessageStart,
		EventID:   newEventID(),
		SessionID: sessionID,
		Message: AssistantContent{
			Role:    string(msg.Role),
			Content: items,
		},
	}
	data, err := json.Marshal(am)
	if err != nil {
		return err
	}

	for _, c := range ch.registry.GetBySession(sessionID) {
		c.TrySend(data)
	}
	return nil
}

// SendEvent converts an engine event to wire-protocol JSON and fans it out to
// all connections for the session. This is the primary streaming path.
func (ch *Channel) SendEvent(_ context.Context, sessionID string, event *types.EngineEvent) error {
	mapper := ch.getOrCreateMapper(sessionID)

	frames, err := mapper.Map(event)
	if err != nil {
		return err
	}

	conns := ch.registry.GetBySession(sessionID)
	for _, frame := range frames {
		for _, c := range conns {
			c.TrySend(frame)
		}
	}

	// Reset mapper on done so the next turn starts fresh.
	if event.Type == types.EngineEventDone {
		mapper.Reset()
	}

	return nil
}

// Health returns nil when the channel is ready.
func (ch *Channel) Health() error {
	if ch.healthy.Load() {
		return nil
	}
	return fmt.Errorf("websocket channel not healthy")
}

// --- internal ---

func (ch *Channel) upgradeHandler(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // allow cross-origin for dev
	})
	if err != nil {
		ch.logger.Warn("websocket upgrade failed", zap.Error(err))
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	userID := r.URL.Query().Get("user_id")
	connID := uuid.New().String()

	conn := newConn(connID, sessionID, userID, ws, ch.logger)
	ch.registry.Register(conn)

	// Send session.created message (v1.1).
	initMsg := SessionCreatedMessage{
		Type:            MsgTypeSessionCreated,
		EventID:         "evt_" + uuid.New().String()[:8],
		SessionID:       sessionID,
		ProtocolVersion: "1.2",
		Session: SessionInfo{
			Capabilities: Capabilities{
				Streaming:   true,
				Tools:       true,
				ClientTools: ch.config.ClientTools,
				MultiTurn:   true,
			},
		},
	}
	if data, err := json.Marshal(initMsg); err == nil {
		conn.TrySend(data)
	}

	ctx := ch.connCtx
	go conn.writePump(ctx)
	go conn.readPump(ctx, ch.handler, ch.abortFn, ch.registry)
}

func (ch *Channel) getOrCreateMapper(sessionID string) *EventMapper {
	if v, ok := ch.mappers.Load(sessionID); ok {
		return v.(*EventMapper)
	}
	m := NewEventMapper(sessionID, ch.config.ClientTools)
	actual, _ := ch.mappers.LoadOrStore(sessionID, m)
	return actual.(*EventMapper)
}
