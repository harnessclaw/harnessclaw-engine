package router_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/audit"
	"harnessclaw-go/internal/engine/agent/scheduler/router"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate/store"
	"harnessclaw-go/internal/msgbus"
	msgstore "harnessclaw-go/internal/msgbus/store"
)

func newTestDeps(t *testing.T) (msgbus.Bus, tstate.Kernel, audit.Logger) {
	t.Helper()
	bus := msgbus.NewInMem(msgstore.NewMemory())
	k := tstate.NewKernel(store.NewMemory(), tstate.KernelConfig{IDGen: tstate.SequentialIDs("t-")})
	log := audit.NewSlogLogger(slog.Default())
	return bus, k, log
}

func TestRouterDispatchesByKind(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, k, log := newTestDeps(t)
	r := router.New(k, bus, log)

	received := make(chan msgbus.AgentMessage, 1)
	r.Handle(msgbus.KindNotify, func(_ context.Context, msg msgbus.AgentMessage) error {
		received <- msg
		return nil
	})

	addr := msgbus.AddrBroadcast("test-session")
	go func() { _ = r.Run(ctx, addr) }()

	// Give the router goroutine time to subscribe.
	time.Sleep(10 * time.Millisecond)

	_ = bus.Publish(ctx, msgbus.AgentMessage{
		MsgID: "m1",
		Kind:  msgbus.KindNotify,
		To:    addr,
	})

	select {
	case msg := <-received:
		if msg.Kind != msgbus.KindNotify {
			t.Fatalf("wrong kind: %s", msg.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestRouterUnknownKindSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, k, log := newTestDeps(t)
	r := router.New(k, bus, log)

	// Register only KindNotify; publish KindControl — should not panic or block.
	notified := make(chan struct{}, 1)
	r.Handle(msgbus.KindNotify, func(_ context.Context, _ msgbus.AgentMessage) error {
		notified <- struct{}{}
		return nil
	})

	addr := msgbus.AddrBroadcast("test-session-2")
	go func() { _ = r.Run(ctx, addr) }()

	time.Sleep(10 * time.Millisecond)

	// Publish unknown kind — must be silently dropped.
	_ = bus.Publish(ctx, msgbus.AgentMessage{MsgID: "m2", Kind: msgbus.KindControl, To: addr})
	// Publish known kind to verify router is still alive.
	_ = bus.Publish(ctx, msgbus.AgentMessage{MsgID: "m3", Kind: msgbus.KindNotify, To: addr})

	select {
	case <-notified:
		// good — router is alive and still dispatching
	case <-time.After(time.Second):
		t.Fatal("timeout: router stopped after unknown kind")
	}
}

func TestRouterHandlerErrorContinuesRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus, k, log := newTestDeps(t)
	r := router.New(k, bus, log)

	count := 0
	r.Handle(msgbus.KindNotify, func(_ context.Context, _ msgbus.AgentMessage) error {
		count++
		if count == 1 {
			return context.DeadlineExceeded // simulate handler error on first call
		}
		return nil
	})

	second := make(chan struct{}, 1)
	r.Handle(msgbus.KindResult, func(_ context.Context, _ msgbus.AgentMessage) error {
		second <- struct{}{}
		return nil
	})

	addr := msgbus.AddrBroadcast("test-session-3")
	go func() { _ = r.Run(ctx, addr) }()
	time.Sleep(10 * time.Millisecond)

	// First message triggers handler error.
	_ = bus.Publish(ctx, msgbus.AgentMessage{MsgID: "m4", Kind: msgbus.KindNotify, To: addr})
	// Second message (different kind) should still be dispatched.
	_ = bus.Publish(ctx, msgbus.AgentMessage{MsgID: "m5", Kind: msgbus.KindResult, To: addr})

	select {
	case <-second:
		// good — routing continued after handler error
	case <-time.After(time.Second):
		t.Fatal("timeout: routing stopped after handler error")
	}
}

func TestRouterCancelExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	bus, k, log := newTestDeps(t)
	r := router.New(k, bus, log)

	done := make(chan error, 1)
	addr := msgbus.AddrBroadcast("test-session-4")
	go func() { done <- r.Run(ctx, addr) }()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: Run did not exit after ctx cancel")
	}
}
