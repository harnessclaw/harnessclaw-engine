package handlers

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnCancellingDrainedHandler processes notify{cancelling_drained}, finalising
// a task from cancelling→cancelled and emitting notify{cancelled} so that
// onTerminal can run parent Resume checks (R5).
// Spec §5.5.5 (v3.1-R5).
type OnCancellingDrainedHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
}

func NewOnCancellingDrained(r tstate.Reader, w tstate.Writer, bus msgbus.Bus) *OnCancellingDrainedHandler {
	return &OnCancellingDrainedHandler{reader: r, writer: w, bus: bus}
}

func (h *OnCancellingDrainedHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	p := msg.Payload.(msgbus.NotifyPayload)
	cur, err := h.reader.Get(ctx, types.TaskID(p.TaskID))
	if err != nil {
		return
	}
	if err := h.writer.ConfirmCancelled(ctx, cur.ID); err != nil {
		return
	}
	// R5: emit notify{cancelled} so onTerminal can run parent Resume checks
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   fmt.Sprintf("%s:cancelled", msg.MsgID),
		Kind:    msgbus.KindNotify,
		To:      msgbus.AddrAgent(p.TaskID),
		TaskID:  p.TaskID,
		Payload: msgbus.NotifyPayload{Event: msgbus.NotifyCancelled, TaskID: p.TaskID},
	})
}
