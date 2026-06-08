package handlers_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/handlers"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

func TestOnSpawnSagaTopologicalDerive(t *testing.T) {
	ctx := context.Background()
	k, bus, _ := setup(t)
	h := handlers.NewOnSpawn(k, k, bus)

	parent, _ := k.Admit(ctx, spec.TaskSpec{Goal: "p"})
	_ = k.MarkReady(ctx, parent)
	_ = k.Claim(ctx, parent, "w", time.Minute, 0)

	// Subscribe to spawn_granted before sending
	grantedCh, c1 := bus.SubscribeOnce(msgbus.FilterNotify(string(parent), msgbus.NotifySpawnGranted))
	defer c1()

	h.Handle(ctx, msgbus.AgentMessage{
		MsgID: "m1", Kind: msgbus.KindControl, TaskID: string(parent),
		Payload: msgbus.ControlPayload{Cmd: msgbus.CmdSpawn, Body: msgbus.SpawnBody{
			Specs: []any{
				spec.TaskSpec{LocalID: "s1", Goal: "step 1"},
				spec.TaskSpec{LocalID: "s2", Goal: "step 2", Deps: []spec.DepRef{"s1"}},
			},
		}},
	})

	select {
	case g := <-grantedCh:
		p := g.Payload.(msgbus.NotifyPayload)
		ids := p.SpawnedIDs
		if len(ids) != 2 {
			t.Fatalf("want 2 spawned, got %d", len(ids))
		}
		// s2 deps must be resolved to s1's TaskID
		s2, _ := k.Get(ctx, types.TaskID(ids[1]))
		if len(s2.Deps) != 1 || string(s2.Deps[0]) != ids[0] {
			t.Fatalf("s2.Deps not resolved: %+v", s2.Deps)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for spawn_granted")
	}
}

func TestOnSpawnRollbackOnDeriveFailure(t *testing.T) {
	ctx := context.Background()
	k, bus, _ := setup(t)
	h := handlers.NewOnSpawn(k, k, bus)

	parent, _ := k.Admit(ctx, spec.TaskSpec{Goal: "p"})
	_ = k.MarkReady(ctx, parent)
	_ = k.Claim(ctx, parent, "w", time.Minute, 0)

	failedCh, c2 := bus.SubscribeOnce(msgbus.FilterNotify(string(parent), msgbus.NotifySpawnFailed))
	defer c2()

	// Second spec has empty Goal — Derive will fail
	h.Handle(ctx, msgbus.AgentMessage{
		MsgID: "m2", Kind: msgbus.KindControl, TaskID: string(parent),
		Payload: msgbus.ControlPayload{Cmd: msgbus.CmdSpawn, Body: msgbus.SpawnBody{
			Specs: []any{
				spec.TaskSpec{LocalID: "s1", Goal: "step 1"},
				spec.TaskSpec{LocalID: "s2", Goal: ""}, // empty Goal → Derive error
			},
		}},
	})

	select {
	case msg := <-failedCh:
		p := msg.Payload.(msgbus.NotifyPayload)
		if p.Event != msgbus.NotifySpawnFailed {
			t.Fatalf("want spawn_failed, got %s", p.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: expected spawn_failed after rollback")
	}

	// Verify s1 was rolled back (store should not find it)
	children, _ := k.ListChildren(ctx, parent)
	for _, c := range children {
		if c.LeafSpec.LocalID == "s1" {
			t.Fatalf("s1 should have been rolled back, but still exists as %s", c.ID)
		}
	}
}
