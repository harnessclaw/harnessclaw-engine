// Package dispatch — spawn helpers.
//
// PublishSpawn and SpawnAndWaitOne are package-level helpers (exported for
// testability and use by the runtime layer in Task 34). Strategies themselves
// call SpawnAndWaitOne via their Deps.Bus; the kernel is NOT involved here —
// child task creation happens in the runtime's onSpawn handler.
package dispatch

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// PublishSpawn sends a control{spawn} message to AddrScheduler requesting the
// creation of a child task described by childSpec under parentID.
//
// Returns the MsgID of the published control message. The actual TaskID of the
// spawned child is assigned by the runtime's onSpawn handler and communicated
// back via a KindNotify{spawn_granted} message.
//
// v3.1-R6: callers that need to observe the result must subscribe BEFORE calling
// PublishSpawn (see SpawnAndWaitOne).
func PublishSpawn(
	ctx context.Context,
	bus msgbus.Bus,
	parentID types.TaskID,
	childSpec spec.TaskSpec,
) (msgID string, err error) {
	id := fmt.Sprintf("ctrl-spawn-%s-%s", parentID, childSpec.LocalID)
	if childSpec.LocalID == "" {
		id = fmt.Sprintf("ctrl-spawn-%s-nolid", parentID)
	}
	msg := msgbus.AgentMessage{
		MsgID:  id,
		Kind:   msgbus.KindControl,
		From:   msgbus.AddrAgent(string(parentID)),
		To:     msgbus.AddrScheduler,
		TaskID: string(parentID),
		Payload: msgbus.ControlPayload{
			Cmd:  msgbus.CmdSpawn,
			Body: msgbus.SpawnBody{Specs: []any{childSpec}},
		},
	}
	if err := bus.Publish(ctx, msg); err != nil {
		return "", fmt.Errorf("dispatch: PublishSpawn: %w", err)
	}
	return id, nil
}

// SpawnAndWaitOne spawns a single child task and blocks until its KindResult
// message arrives (or ctx is cancelled).
//
// Subscribe-before-Publish ordering (v3.1-R6): the function subscribes for ALL
// messages on the parent agent's address BEFORE publishing the spawn request.
// This single subscription captures both the KindNotify{spawn_granted} (which
// reveals the childID) and later the KindResult for the child — eliminating the
// race where the result arrives before we can subscribe.
//
// parentID is used as the listening address; callers that need kernel.Park
// semantics must call it themselves after SpawnAndWaitOne returns — this helper
// is bus-only and has no kernel access.
func SpawnAndWaitOne(
	ctx context.Context,
	bus msgbus.Bus,
	parentID types.TaskID,
	childSpec spec.TaskSpec,
) (msgbus.ResultMessage, error) {
	// R6: Subscribe broadly BEFORE publishing so we never miss the grant or result.
	// AddrScheduler is the catchall — InMemBus fans out every non-queue message here.
	// We do our own Kind/event/taskID filtering below to distinguish the two phases.
	msgCh, cancelSub := bus.Subscribe(msgbus.AddrScheduler)
	defer cancelSub()

	if _, err := PublishSpawn(ctx, bus, parentID, childSpec); err != nil {
		return msgbus.ResultMessage{}, err
	}

	// Phase 1: wait for spawn_granted to learn childID.
	var childID string
	for {
		select {
		case <-ctx.Done():
			return msgbus.ResultMessage{}, fmt.Errorf("dispatch: SpawnAndWaitOne: context cancelled waiting for spawn_granted: %w", ctx.Err())
		case msg := <-msgCh:
			if msg.Kind != msgbus.KindNotify {
				continue
			}
			p, ok := msg.Payload.(msgbus.NotifyPayload)
			if !ok || p.Event != msgbus.NotifySpawnGranted || msg.TaskID != string(parentID) {
				continue
			}
			if len(p.SpawnedIDs) == 0 {
				return msgbus.ResultMessage{}, fmt.Errorf("dispatch: SpawnAndWaitOne: spawn_granted has no SpawnedIDs")
			}
			childID = p.SpawnedIDs[0]
		}
		if childID != "" {
			break
		}
	}

	// Phase 2: wait for the child's KindResult.
	// The subscription at AddrAgent(parentID) also receives messages fanned out
	// via AddrScheduler, which includes KindResult messages for any task.
	for {
		select {
		case <-ctx.Done():
			return msgbus.ResultMessage{}, fmt.Errorf("dispatch: SpawnAndWaitOne: context cancelled waiting for result of %s: %w", childID, ctx.Err())
		case msg := <-msgCh:
			if msg.Kind != msgbus.KindResult || msg.TaskID != childID {
				continue
			}
			rm, ok := msg.Payload.(msgbus.ResultMessage)
			if !ok {
				return msgbus.ResultMessage{}, fmt.Errorf("dispatch: SpawnAndWaitOne: malformed result payload for child %s", childID)
			}
			return rm, nil
		}
	}
}
