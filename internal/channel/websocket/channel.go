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
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/channel/websocket/internal/toolphrase"
	emitv2 "harnessclaw-go/internal/channel/emit/v2"
	"harnessclaw-go/internal/humanloop"
	"harnessclaw-go/internal/humanloop/wait"
	"harnessclaw-go/pkg/types"
)

// Channel is the v2.2 WebSocket Duplex adapter. Implements channel.Duplex.
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
	prompter *humanloop.Prompter
	resumer  wait.Resumer

	// messages is the inbound channel exposed by Duplex.Messages.
	// Every user.message / tool.result / permission.response /
	// plan.response / step_decision frame read on a conn is enqueued
	// here; the consumer (typically the router goroutine in main.go)
	// drains it via for-range. Capacity 128 absorbs short bursts so a
	// slow consumer doesn't backpressure the conn read loop.
	messages chan *types.IncomingMessage

	healthy   atomic.Bool
	closeOnce sync.Once

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
	// toolphrase.Picker resolves localized phase hints for tool cards and the M4
	// inter-round thinking hint on message cards. Per-session randomness
	// is seeded fresh on each new session via the rng factory below;
	// production wants non-deterministic rotation, tests use a fixed seed
	// via NewTranslator(picker) directly.
	picker := toolphrase.NewPicker(func() *rand.Rand {
		return rand.New(rand.NewSource(time.Now().UnixNano()))
	})
	return &Channel{
		cfg:        cfg,
		logger:     logger.Named("ws"),
		registry:   newConnRegistry(),
		translator: NewTranslator(picker),
		sequencer:  emitv2.NewSequencer(),
		tracker:    emitv2.NewTracker(emitv2.TrackerConfig{CheckEvery: time.Second}),
		messages:   make(chan *types.IncomingMessage, 128),
	}
}

// SetPrompter wires a Prompter into the channel. With a Prompter set,
// the translator persists every wait before emitting, and conn falls
// through to the persisted-wait path when a user reply arrives for an
// in-memory miss. Call before Start.
func (c *Channel) SetPrompter(p *humanloop.Prompter) { c.prompter = p }

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

// Start implements channel.Channel — non-blocking. Boots the HTTP
// listener and returns. Server exit errors are surfaced via the logger;
// the caller drives shutdown via ctx.Done() or Close().
func (c *Channel) Start(ctx context.Context) error {
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

	go func() {
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			c.logger.Error("websocket server exited", zap.Error(err))
		}
	}()

	return nil
}

// Messages implements channel.Inbound. Returns the inbound channel,
// which is closed by Close().
func (c *Channel) Messages() <-chan *types.IncomingMessage { return c.messages }

// Close implements channel.Channel — graceful shutdown. Stops the HTTP
// server, drains live connections, and closes the messages channel so
// for-range consumers exit. Idempotent: safe to call more than once.
func (c *Channel) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.healthy.Store(false)
		if c.connCanc != nil {
			c.connCanc()
		}
		c.tracker.Stop()
		c.registry.closeAll()
		close(c.messages)
		if c.server != nil {
			err = c.server.Shutdown(context.Background())
		}
	})
	return err
}

// Reply implements channel.Replier. Drains Outbound.Stream until the
// caller closes it, then processes Final. Stream and Final can each be
// supplied independently or together.
func (c *Channel) Reply(ctx context.Context, sessionID string, msg channel.Outbound) error {
	em := c.emitterFor(sessionID)
	if em == nil {
		// No Emitter for this session (the connection already
		// dropped) — drain Stream to unblock the producer.
		if msg.Stream != nil {
			for range msg.Stream {
			}
		}
		return nil
	}

	if msg.Stream != nil {
		for evt := range msg.Stream {
			evt := evt
			c.translator.Translate(em, sessionID, &evt)
		}
	}

	if msg.Final != nil {
		turnID := emitv2.NewCardID(emitv2.CardTurn)
		msgID := emitv2.NewCardID(emitv2.CardMessage)
		em.Card(emitv2.CardTurn, turnID).Add(emitv2.TurnPayload{TurnNo: 1})
		em.Card(emitv2.CardMessage, msgID).Add(
			emitv2.MessagePayload{Role: string(msg.Final.Role)},
			emitv2.WithParent(turnID),
		)
		for _, cb := range msg.Final.Content {
			if cb.Text != "" {
				em.Card(emitv2.CardMessage, msgID).Append(emitv2.ChannelText, cb.Text)
			}
		}
		em.Card(emitv2.CardMessage, msgID).Close(emitv2.StatusOK)
		em.Card(emitv2.CardTurn, turnID).Close(emitv2.StatusOK)
	}

	return nil
}

// SendEvent is a test / internal helper that pushes a single
// EngineEvent to a session's Emitter. Production code should use
// Reply(ctx, sessionID, Outbound{Stream: ...}) instead.
//
// Deprecated: not part of the channel.Duplex contract; retained only so
// channel_test.go / recovery_test.go can inject single events directly
// for behavioral assertions.
func (c *Channel) SendEvent(_ context.Context, sessionID string, event *types.EngineEvent) error {
	em := c.emitterFor(sessionID)
	if em == nil {
		return nil
	}
	c.translator.Translate(em, sessionID, event)
	return nil
}

// Stop is an alias for Close kept around so existing test code that
// uses the old name keeps compiling.
//
// Deprecated: use Close() instead.
func (c *Channel) Stop(_ context.Context) error { return c.Close() }

// publish is called by conn to enqueue an inbound frame into the
// messages channel. Returns an error if the channel is full or the
// context is cancelled; the caller decides whether to surface the
// error back to the client.
func (c *Channel) publish(ctx context.Context, in *types.IncomingMessage) error {
	select {
	case c.messages <- in:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.connCtx.Done():
		return c.connCtx.Err()
	}
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
