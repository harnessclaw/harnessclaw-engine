package handlers_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/handlers"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/msgbus"
)

func TestOnTerminalSucceededTriggersKindTask(t *testing.T) {
	ctx := context.Background()
	k, bus, _ := setup(t)
	h := handlers.NewOnTerminal(k, k, bus)

	parent, _ := k.Admit(ctx, spec.TaskSpec{Goal: "p"})
	_ = k.MarkReady(ctx, parent)
	_ = k.Claim(ctx, parent, "w", time.Minute, 0)

	dep, _ := k.Derive(ctx, parent, spec.TaskSpec{Goal: "dep", LocalID: "s1"})
	_ = k.MarkReady(ctx, dep)
	_ = k.Claim(ctx, dep, "w", time.Minute, 0)
	_ = k.Succeed(ctx, dep, "meta.json")

	child, _ := k.Derive(ctx, parent, spec.TaskSpec{Goal: "child", Deps: []spec.DepRef{spec.DepRef(string(dep))}})

	// Subscribe to KindTask BEFORE invoking the handler
	taskCh, cancel := bus.Subscribe(msgbus.AddrQueue("leaf"))
	defer cancel()

	h.Handle(ctx, msgbus.NotifySucceeded, dep)

	select {
	case msg := <-taskCh:
		p := msg.Payload.(msgbus.TaskMessage)
		if p.TaskID != string(child) {
			t.Fatalf("want child task in queue, got %s", p.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: onTerminal must publish KindTask to queue:leaf (R8)")
	}
}
