// Package router subscribes to the scheduler's bus address and dispatches
// incoming AgentMessages to registered handlers based on msg.Kind.
package router

import (
	"context"
	"log/slog"

	"harnessclaw-go/internal/engine/scheduler/audit"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/msgbus"
)

// HandlerFn processes a single AgentMessage for a registered Kind.
type HandlerFn func(ctx context.Context, msg msgbus.AgentMessage) error

// Router subscribes to a Bus address and dispatches messages by Kind.
type Router struct {
	kernel   tstate.Kernel
	bus      msgbus.Bus
	log      audit.Logger
	handlers map[msgbus.Kind]HandlerFn
}

// New creates a Router with the given dependencies.
func New(kernel tstate.Kernel, bus msgbus.Bus, log audit.Logger) *Router {
	return &Router{
		kernel:   kernel,
		bus:      bus,
		log:      log,
		handlers: make(map[msgbus.Kind]HandlerFn),
	}
}

// Handle registers fn as the handler for the given Kind.
// Calling Handle with the same Kind twice overwrites the previous handler.
func (r *Router) Handle(kind msgbus.Kind, fn HandlerFn) {
	r.handlers[kind] = fn
}

// Run subscribes to addr and dispatches messages until ctx is done or the
// channel is closed. If ready is non-nil it is closed immediately after the
// bus subscription is established, allowing callers to synchronise startup.
//
// Self-review checklist:
//   - ctx cancel → returns ctx.Err() cleanly
//   - Unknown Kinds → silently skipped, no panic
//   - Handler errors → logged but routing continues
func (r *Router) Run(ctx context.Context, addr msgbus.Address, ready ...chan<- struct{}) error {
	ch, cancel := r.bus.Subscribe(addr)
	defer cancel()

	// Signal readiness immediately after subscription is established so that
	// callers waiting on the channel know messages will not be missed.
	if len(ready) > 0 && ready[0] != nil {
		close(ready[0])
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			fn, exists := r.handlers[msg.Kind]
			if !exists {
				continue // unknown Kind — silently skip
			}
			if err := fn(ctx, msg); err != nil {
				r.log.Log(ctx, "router_error",
					slog.String("kind", string(msg.Kind)),
					slog.String("msg_id", msg.MsgID),
					slog.String("error", err.Error()),
				)
				// continue routing — one bad message must not stop the loop
			}
		}
	}
}
