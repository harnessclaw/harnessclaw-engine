package handlers

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnCompletedFromStagingHandler processes notify{completed_from_staging}, reading
// the StagedResultRef and calling ConfirmSucceededFromStaging.
// Spec §5.5.5.
type OnCompletedFromStagingHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
}

func NewOnCompletedFromStaging(r tstate.Reader, w tstate.Writer, bus msgbus.Bus) *OnCompletedFromStagingHandler {
	return &OnCompletedFromStagingHandler{reader: r, writer: w, bus: bus}
}

func (h *OnCompletedFromStagingHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	p := msg.Payload.(msgbus.NotifyPayload)
	cur, err := h.reader.Get(ctx, types.TaskID(p.TaskID))
	if err != nil || cur.StagedResultRef == "" {
		return
	}
	if err := h.writer.ConfirmSucceededFromStaging(ctx, cur.ID, cur.StagedResultRef, cur.Attempt); err != nil {
		return
	}
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   fmt.Sprintf("%s:succeeded", msg.MsgID),
		Kind:    msgbus.KindNotify,
		To:      msgbus.AddrAgent(p.TaskID),
		TaskID:  p.TaskID,
		Payload: msgbus.NotifyPayload{Event: msgbus.NotifySucceeded, TaskID: p.TaskID},
	})
}
