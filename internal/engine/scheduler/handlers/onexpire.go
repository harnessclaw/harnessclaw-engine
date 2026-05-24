package handlers

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnExpireHandler processes notify{lease_expired|deadline_exceeded} messages
// from the reaper, calling Writer.Expire to drive FailOrRetry.
// Spec §5.5.5.
type OnExpireHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
}

func NewOnExpire(r tstate.Reader, w tstate.Writer, bus msgbus.Bus) *OnExpireHandler {
	return &OnExpireHandler{reader: r, writer: w, bus: bus}
}

func (h *OnExpireHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	p := msg.Payload.(msgbus.NotifyPayload)
	var reason types.FailureReason
	switch p.Event {
	case msgbus.NotifyLeaseExpired:
		reason = types.FailLeaseExpired
	case msgbus.NotifyDeadlineExceeded:
		reason = types.FailDeadlineExceeded
	default:
		return
	}
	cur, err := h.reader.Get(ctx, types.TaskID(p.TaskID))
	if err != nil {
		return
	}
	if err := h.writer.Expire(ctx, cur.ID, reason, cur.Attempt); err != nil {
		return
	}
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   fmt.Sprintf("%s:failed", msg.MsgID),
		Kind:    msgbus.KindNotify,
		To:      msgbus.AddrAgent(p.TaskID),
		TaskID:  p.TaskID,
		Payload: msgbus.NotifyPayload{Event: msgbus.NotifyFailed, TaskID: p.TaskID, Reason: string(reason)},
	})
}
