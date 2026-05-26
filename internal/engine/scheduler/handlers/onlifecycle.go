package handlers

import (
	"context"
	"fmt"
	"log/slog"

	"harnessclaw-go/internal/engine/scheduler/audit"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnLifecycleHandler processes KindLifecycle messages from L3 sub-agents.
// Validates From address and epoch (Attempt) before mutating task state.
// Spec §5.5.1 (v3.1-R11).
type OnLifecycleHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
	audit  audit.Logger
}

func NewOnLifecycle(r tstate.Reader, w tstate.Writer, bus msgbus.Bus, au audit.Logger) *OnLifecycleHandler {
	return &OnLifecycleHandler{reader: r, writer: w, bus: bus, audit: au}
}

func (h *OnLifecycleHandler) Handle(ctx context.Context, msg msgbus.AgentMessage) {
	p, ok := msg.Payload.(msgbus.LifecyclePayload)
	if !ok {
		h.audit.Log(ctx, "onlifecycle.bad_payload", slog.String("msg_id", msg.MsgID))
		return
	}
	id := types.TaskID(msg.TaskID)

	// R11: validate From must equal "agent:<task_id>"
	if msg.From != msgbus.AddrAgent(msg.TaskID) {
		h.audit.Log(ctx, "onlifecycle.drop_bad_from",
			slog.String("task_id", msg.TaskID),
			slog.String("from", string(msg.From)),
		)
		return
	}

	// epoch guard: attempt must match current task attempt
	cur, err := h.reader.Get(ctx, id)
	if err != nil {
		h.audit.Log(ctx, "onlifecycle.get_failed",
			slog.String("task_id", msg.TaskID),
			slog.String("err", err.Error()),
		)
		return
	}
	if p.Attempt != cur.Attempt {
		h.audit.Log(ctx, "onlifecycle.drop_stale_attempt",
			slog.String("task_id", msg.TaskID),
			slog.Int("msg_attempt", p.Attempt),
			slog.Int("cur_attempt", cur.Attempt),
		)
		return
	}

	switch p.Event {
	case msgbus.EventStarted:
		// Transition ready→running by claiming the lease. The worker identity is
		// derived from the From address ("agent:<task_id>"). Zero lease duration
		// uses the kernel's DefaultLeaseTTL.
		if err := h.writer.Claim(ctx, id, string(msg.From), 0, p.Attempt); err != nil {
			h.audit.Log(ctx, "onlifecycle.claim_failed",
				slog.String("task_id", msg.TaskID),
				slog.String("err", err.Error()),
			)
			// Don't return — Claim may fail if already claimed (idempotency), which is fine.
		}

	case msgbus.EventHeartbeat:
		// Renew the lease so the reaper doesn't evict an actively-running task.
		if err := h.writer.RenewLease(ctx, id, string(msg.From)); err != nil {
			// Lease renewal failure is not fatal; the reaper will handle expiry.
			h.audit.Log(ctx, "onlifecycle.renew_lease_failed",
				slog.String("task_id", msg.TaskID),
				slog.String("err", err.Error()),
			)
		}

	case msgbus.EventCompleted:
		if err := h.writer.Succeed(ctx, id, types.Ref(p.ResultRef)); err != nil {
			h.audit.Log(ctx, "onlifecycle.succeed_failed",
				slog.String("task_id", msg.TaskID),
				slog.String("err", err.Error()),
			)
			return
		}
		_ = h.bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:   fmt.Sprintf("%s:succeeded", msg.MsgID),
			Kind:    msgbus.KindNotify,
			To:      msgbus.AddrAgent(msg.TaskID),
			TaskID:  msg.TaskID,
			Payload: msgbus.NotifyPayload{Event: msgbus.NotifySucceeded, TaskID: msg.TaskID},
		})

	case msgbus.EventSpawned:
		// Transition running→waiting so the parent parks until all children finish.
		// SpawnedIDs lists the child TaskIDs that must complete before the parent wakes.
		waitingFor := make([]types.TaskID, len(p.SpawnedIDs))
		for i, sid := range p.SpawnedIDs {
			waitingFor[i] = types.TaskID(sid)
		}
		if err := h.writer.Park(ctx, id, waitingFor); err != nil {
			h.audit.Log(ctx, "onlifecycle.park_failed",
				slog.String("task_id", msg.TaskID),
				slog.String("err", err.Error()),
			)
		}

	case msgbus.EventFailed:
		reason := types.FailureReason(p.FailureReason)
		if reason == "" {
			reason = types.FailWorkerError
		}
		if err := h.writer.FailOrRetry(ctx, id, reason, p.ErrMsg, p.Attempt); err != nil {
			h.audit.Log(ctx, "onlifecycle.failorretry_failed",
				slog.String("task_id", msg.TaskID),
				slog.String("err", err.Error()),
			)
			return
		}
		_ = h.bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:  fmt.Sprintf("%s:failed", msg.MsgID),
			Kind:   msgbus.KindNotify,
			To:     msgbus.AddrAgent(msg.TaskID),
			TaskID: msg.TaskID,
			Payload: msgbus.NotifyPayload{
				Event:  msgbus.NotifyFailed,
				TaskID: msg.TaskID,
				Reason: string(reason),
			},
		})
	}
}
