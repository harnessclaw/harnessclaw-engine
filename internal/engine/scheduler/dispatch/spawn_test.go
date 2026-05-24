package dispatch_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/msgbus/store"
)

// newTestBus returns an in-memory Bus for tests.
func newTestBus() msgbus.Bus {
	return msgbus.NewInMem(store.NewMemory())
}

// TestPublishSpawn_SendsControlMessage verifies that PublishSpawn publishes a
// KindControl/CmdSpawn message addressed to AddrScheduler.
func TestPublishSpawn_SendsControlMessage(t *testing.T) {
	ctx := context.Background()
	bus := newTestBus()

	ch, cancel := bus.Subscribe(msgbus.AddrScheduler)
	defer cancel()

	msgID, err := dispatch.PublishSpawn(ctx, bus, "t-parent", spec.TaskSpec{LocalID: "leaf-0", Goal: "do thing"})
	if err != nil {
		t.Fatalf("PublishSpawn: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty msgID")
	}

	select {
	case msg := <-ch:
		if msg.Kind != msgbus.KindControl {
			t.Fatalf("expected KindControl, got %s", msg.Kind)
		}
		if msg.To != msgbus.AddrScheduler {
			t.Fatalf("expected To=scheduler, got %s", msg.To)
		}
		cp, ok := msg.Payload.(msgbus.ControlPayload)
		if !ok {
			t.Fatalf("expected ControlPayload, got %T", msg.Payload)
		}
		if cp.Cmd != msgbus.CmdSpawn {
			t.Fatalf("expected CmdSpawn, got %s", cp.Cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for control message")
	}
}

// TestPublishSpawn_UniqueParentLocalID checks that two spawns with distinct
// LocalIDs produce distinct MsgIDs (no accidental dedup).
func TestPublishSpawn_UniqueParentLocalID(t *testing.T) {
	ctx := context.Background()
	bus := newTestBus()

	id1, err := dispatch.PublishSpawn(ctx, bus, "t-parent", spec.TaskSpec{LocalID: "leaf-0", Goal: "a"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := dispatch.PublishSpawn(ctx, bus, "t-parent", spec.TaskSpec{LocalID: "leaf-1", Goal: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct msg IDs, both got %q", id1)
	}
}

// TestSpawnAndWaitOne_SubscribeBeforePublish is the primary R6 test:
// a simulated runtime goroutine intercepts the control{spawn} and immediately
// replies with spawn_granted + result. SpawnAndWaitOne must receive both.
func TestSpawnAndWaitOne_SubscribeBeforePublish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	bus := newTestBus()
	parentID := types.TaskID("t-parent")

	// Subscribe to AddrScheduler BEFORE launching the goroutine to avoid
	// a race where SpawnAndWaitOne publishes before the goroutine is scheduled.
	schedulerCh, cancelSched := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSched()

	// Simulate onSpawn runtime: wait for control{spawn}, reply with grant + result.
	go func() {
		select {
		case <-ctx.Done():
			return
		case msg := <-schedulerCh:
			if msg.Kind != msgbus.KindControl {
				return
			}
			childID := "t-child-1"

			// Publish spawn_granted notify to parent's agent address.
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  "notify-grant-1",
				Kind:   msgbus.KindNotify,
				To:     msgbus.AddrAgent(string(parentID)),
				TaskID: string(parentID),
				Payload: msgbus.NotifyPayload{
					Event:      msgbus.NotifySpawnGranted,
					TaskID:     string(parentID),
					SpawnedIDs: []string{childID},
				},
			})

			// Publish result for child.
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  "result-child-1",
				Kind:   msgbus.KindResult,
				To:     msgbus.AddrAgent(childID),
				TaskID: childID,
				Payload: msgbus.ResultMessage{
					TaskID:     childID,
					TaskType:   "leaf",
					OutputFile: "meta.json",
					Status:     msgbus.ResultStatusDone,
					Summary:    "all done",
				},
			})
		}
	}()

	result, err := dispatch.SpawnAndWaitOne(ctx, bus, parentID, spec.TaskSpec{LocalID: "leaf-0", Goal: "x"})
	if err != nil {
		t.Fatalf("SpawnAndWaitOne: %v", err)
	}
	if result.OutputFile != "meta.json" {
		t.Fatalf("expected meta.json, got %q", result.OutputFile)
	}
	if result.Status != msgbus.ResultStatusDone {
		t.Fatalf("expected done, got %q", result.Status)
	}
}

// TestSpawnAndWaitOne_CtxCancel verifies that cancelling the context causes
// SpawnAndWaitOne to return promptly with an error.
func TestSpawnAndWaitOne_CtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bus := newTestBus()

	// Cancel immediately — no runtime goroutine will respond.
	cancel()

	_, err := dispatch.SpawnAndWaitOne(ctx, bus, "t-parent", spec.TaskSpec{LocalID: "leaf-0", Goal: "x"})
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

// TestSpawnAndWaitOne_FailedStatus verifies that a failed result is returned
// without error (error is reserved for bus/ctx failures, not task failures).
func TestSpawnAndWaitOne_FailedStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	bus := newTestBus()
	parentID := types.TaskID("t-parent-2")

	// Subscribe before goroutine launch to avoid scheduling race.
	schedulerCh, cancelSched := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSched()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-schedulerCh:
			childID := "t-child-failed"
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  "notify-grant-2",
				Kind:   msgbus.KindNotify,
				To:     msgbus.AddrAgent(string(parentID)),
				TaskID: string(parentID),
				Payload: msgbus.NotifyPayload{
					Event:      msgbus.NotifySpawnGranted,
					TaskID:     string(parentID),
					SpawnedIDs: []string{childID},
				},
			})
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  "result-child-failed",
				Kind:   msgbus.KindResult,
				To:     msgbus.AddrAgent(childID),
				TaskID: childID,
				Payload: msgbus.ResultMessage{
					TaskID: childID, TaskType: "leaf",
					Status: msgbus.ResultStatusFailed,
					Reason: "tool_error: something broke",
				},
			})
		}
	}()

	result, err := dispatch.SpawnAndWaitOne(ctx, bus, parentID, spec.TaskSpec{LocalID: "leaf-f", Goal: "fail"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != msgbus.ResultStatusFailed {
		t.Fatalf("expected failed status, got %q", result.Status)
	}
}
