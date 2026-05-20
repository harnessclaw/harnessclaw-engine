// Package websocket implements the v2.2 card-based WebSocket channel.
//
// One protocol, one endpoint, one Channel type. Outbound events are
// emitv2.Event JSON frames; inbound frames are user.message /
// tool.result / prompt.user_response / session.resume / session.interrupt
// / session.create / ping.
//
// The engine still emits *types.EngineEvent via channel.Channel.SendEvent;
// translator.go converts those into emitv2 Builder calls so engine code
// doesn't need to be rewired in this revision.
package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/config"
	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/internal/engine/prompter"
	"harnessclaw-go/internal/engine/wait"
	"harnessclaw-go/pkg/types"
)

// Channel is the v2.2 WebSocket channel. Implements channel.Channel.
type Channel struct {
	cfg    config.WSChannelConfig
	logger *zap.Logger

	server     *http.Server
	registry   *connRegistry
	translator *Translator
	sequencer  *emitv2.Sequencer
	tracker    *emitv2.Tracker

	// Recovery: prompter persists outstanding waits so a server restart
	// between emit and reply does not lose the conversation; resumer is
	// the engine-side hook that re-enters the query loop with the
	// synthesised tool_result when a user replies post-restart.
	//
	// Both are optional: when nil the channel skips persistence and
	// recovery (matches the alpha-without-recovery behaviour). Wire
	// them in through SetPrompter / SetResumer.
	prompter *prompter.Prompter
	resumer  wait.Resumer

	handler channel.MessageHandler // engine inbound dispatch (set by Start)
	healthy atomic.Bool

	connCtx  context.Context
	connCanc context.CancelFunc
}

// New constructs a v2.2 WebSocket channel. Signature mirrors the legacy
// v1 constructor so cmd/server/main.go does not need updating. The
// abortFn is currently unused — interrupt is signalled via session.interrupt
// frames in v2.2.
func New(cfg config.WSChannelConfig, _ func(context.Context, string) error, logger *zap.Logger) *Channel {
	if cfg.Path == "" {
		cfg.Path = "/v1/ws"
	}
	return &Channel{
		cfg:        cfg,
		logger:     logger.Named("ws"),
		registry:   newConnRegistry(),
		translator: NewTranslator(nil),
		sequencer:  emitv2.NewSequencer(),
		tracker:    emitv2.NewTracker(emitv2.TrackerConfig{CheckEvery: time.Second}),
	}
}

// SetPrompter wires a Prompter into the channel. With a Prompter set,
// the translator persists every wait before emitting, and conn falls
// through to the persisted-wait path when a user reply arrives for an
// in-memory miss. Call before Start.
func (c *Channel) SetPrompter(p *prompter.Prompter) { c.prompter = p }

// SetResumer wires the engine-side resume callback. Without a Resumer
// the channel can persist waits and re-emit them on reconnect, but
// cannot drive the engine to actually consume the answer post-restart
// — replies for unknown live waiters get rejected with an error frame.
// Call before Start.
func (c *Channel) SetResumer(r wait.Resumer) { c.resumer = r }

// GetTranslator returns the channel's translator so callers (cmd/server
// in particular) can wire recovery dependencies (SetIssuer) without
// the channel needing to plumb them through its own constructor.
func (c *Channel) GetTranslator() *Translator { return c.translator }

// Name implements channel.Channel.
func (c *Channel) Name() string { return "websocket" }

// Health implements channel.Channel.
func (c *Channel) Health() error {
	if c.healthy.Load() {
		return nil
	}
	return fmt.Errorf("websocket channel not healthy")
}

// Start implements channel.Channel. Boots the HTTP listener, binds the
// upgrade handler, and blocks until ctx is cancelled.
func (c *Channel) Start(ctx context.Context, handler channel.MessageHandler) error {
	c.handler = handler
	c.connCtx, c.connCanc = context.WithCancel(ctx)
	c.tracker.Start()

	mux := http.NewServeMux()
	mux.HandleFunc(c.cfg.Path, c.upgrade)

	addr := fmt.Sprintf("%s:%d", c.cfg.Host, c.cfg.Port)
	c.server = &http.Server{Addr: addr, Handler: mux}
	c.healthy.Store(true)
	c.logger.Info("websocket channel listening",
		zap.String("path", c.cfg.Path),
		zap.String("addr", addr),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return c.Stop(context.Background())
	case err := <-errCh:
		return fmt.Errorf("websocket server error: %w", err)
	}
}

// Stop implements channel.Channel.
func (c *Channel) Stop(ctx context.Context) error {
	c.healthy.Store(false)
	if c.connCanc != nil {
		c.connCanc()
	}
	c.tracker.Stop()
	c.registry.closeAll()
	if c.server != nil {
		return c.server.Shutdown(ctx)
	}
	return nil
}

// Send implements channel.Channel — non-streaming pre-assembled message.
// Emits a one-shot turn → message → close on the session's Emitter.
func (c *Channel) Send(_ context.Context, sessionID string, msg *types.Message) error {
	em := c.emitterFor(sessionID)
	if em == nil {
		return nil
	}
	turnID := emitv2.NewCardID(emitv2.CardTurn)
	msgID := emitv2.NewCardID(emitv2.CardMessage)
	em.Card(emitv2.CardTurn, turnID).Add(emitv2.TurnPayload{TurnNo: 1})
	em.Card(emitv2.CardMessage, msgID).Add(emitv2.MessagePayload{Role: string(msg.Role)},
		emitv2.WithParent(turnID))
	for _, cb := range msg.Content {
		if cb.Text != "" {
			em.Card(emitv2.CardMessage, msgID).Append(emitv2.ChannelText, cb.Text)
		}
	}
	em.Card(emitv2.CardMessage, msgID).Close(emitv2.StatusOK)
	em.Card(emitv2.CardTurn, turnID).Close(emitv2.StatusOK)
	return nil
}

// SendEvent implements channel.Channel — primary streaming path.
// Translates v1 EngineEvent → v2.2 Builder calls.
func (c *Channel) SendEvent(_ context.Context, sessionID string, event *types.EngineEvent) error {
	em := c.emitterFor(sessionID)
	if em == nil {
		return nil
	}
	c.translator.Translate(em, sessionID, event)
	return nil
}

// emitterFor returns the Emitter bound to the first connection of
// sessionID, or nil if no connection is registered.
func (c *Channel) emitterFor(sessionID string) *emitv2.Emitter {
	conns := c.registry.bySession(sessionID)
	if len(conns) == 0 {
		return nil
	}
	return conns[0].emitter
}

// upgrade is the http.HandlerFunc for the WS endpoint.
func (c *Channel) upgrade(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // dev convenience; prod uses reverse-proxy
	})
	if err != nil {
		c.logger.Warn("ws upgrade failed", zap.Error(err))
		return
	}

	// Lift the per-frame read limit so multimodal user.message frames
	// (base64-encoded images up to 20 MB aggregate, see
	// multimodal.MaxTotalBytesPerMessage) aren't truncated at the
	// nhooyr default of 32 KB. 32 MB leaves headroom over the
	// engine-side cap for envelope + tool-result frames; the engine's
	// own conn.checkInlineSizeCaps still rejects anything past
	// MaxTotalBytesPerMessage, so this is a transport ceiling only,
	// not a policy change.
	ws.SetReadLimit(32 * 1024 * 1024)

	connID := uuid.New().String()
	conn := &Conn{
		id:     connID,
		ws:     ws,
		send:   make(chan []byte, sendBufSize),
		done:   make(chan struct{}),
		ch:     c,
		logger: c.logger.With(zap.String("conn", connID)),
	}

	go conn.writePump(c.connCtx)
	go conn.readPump(c.connCtx)
}

// connRegistry is a sessionID → live connections fan-out index.
type connRegistry struct {
	mu sync.RWMutex
	m  map[string]map[string]*Conn // sessionID → connID → *Conn
}

func newConnRegistry() *connRegistry {
	return &connRegistry{m: make(map[string]map[string]*Conn)}
}

func (r *connRegistry) register(sessionID string, c *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bag, ok := r.m[sessionID]
	if !ok {
		bag = make(map[string]*Conn)
		r.m[sessionID] = bag
	}
	bag[c.id] = c
}

func (r *connRegistry) unregister(sessionID, connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bag, ok := r.m[sessionID]; ok {
		delete(bag, connID)
		if len(bag) == 0 {
			delete(r.m, sessionID)
		}
	}
}

func (r *connRegistry) bySession(sessionID string) []*Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bag := r.m[sessionID]
	out := make([]*Conn, 0, len(bag))
	for _, c := range bag {
		out = append(out, c)
	}
	return out
}

func (r *connRegistry) closeAll() {
	r.mu.Lock()
	bags := r.m
	r.m = make(map[string]map[string]*Conn)
	r.mu.Unlock()
	for _, bag := range bags {
		for _, c := range bag {
			c.close()
		}
	}
}

// jsonMust marshals v or panics — used only on shapes we control.
func jsonMust(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
