package handlers

import (
	"context"
	"fmt"
	"log/slog"

	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// OnTerminalHandler is called internally (not via bus routing) whenever a task
// becomes terminal. It drives two spec requirements:
//   - R8: when a dep becomes terminal (succeeded), find pending children that
//     listed it as a dep; MarkReady those whose other deps are also terminal,
//     then publish KindTask to queue:leaf to close the dispatch loop.
//   - R5: if the completed task's parent is Waiting and all of its WaitingFor
//     children are now terminal, call kernel.Resume on the parent.
//
// Spec §5.5.2 (v3.1-R5/R8/R11).
type OnTerminalHandler struct {
	reader tstate.Reader
	writer tstate.Writer
	bus    msgbus.Bus
}

func NewOnTerminal(r tstate.Reader, w tstate.Writer, bus msgbus.Bus) *OnTerminalHandler {
	return &OnTerminalHandler{reader: r, writer: w, bus: bus}
}

// Handle is called with the notify event and the ID of the task that just became terminal.
func (h *OnTerminalHandler) Handle(ctx context.Context, event msgbus.NotifyEvent, taskID types.TaskID) {
	// R8: for every pending task that listed taskID as a dep, try MarkReady.
	pendingDeps, err := h.reader.ListPendingDependentOn(ctx, taskID)
	if err != nil {
		slog.ErrorContext(ctx, "onterminal.list_pending_dependent_on",
			slog.String("task_id", string(taskID)),
			slog.String("err", err.Error()),
		)
		return
	}

	for _, dependent := range pendingDeps {
		if err := h.writer.MarkReady(ctx, dependent.ID); err != nil {
			// MarkReady will fail if some other dep is not yet succeeded — that is normal.
			continue
		}
		// Task became ready: publish KindTask to close the dispatch loop (R8).
		cur, readErr := h.reader.Get(ctx, dependent.ID)
		if readErr != nil {
			continue
		}
		_ = h.bus.Publish(ctx, msgbus.AgentMessage{
			MsgID:  fmt.Sprintf("task:%s:ready", string(dependent.ID)),
			Kind:   msgbus.KindTask,
			To:     msgbus.AddrQueue("leaf"),
			TaskID: string(dependent.ID),
			Payload: msgbus.TaskMessage{
				TaskID:   string(dependent.ID),
				TaskType: "leaf",
				Task:     cur.LeafSpec.Goal,
			},
		})
	}

	// R5: check if taskID's parent is Waiting; if all WaitingFor are terminal, Resume.
	cur, err := h.reader.Get(ctx, taskID)
	if err != nil {
		return
	}
	if cur.ParentID == "" {
		return
	}
	parent, err := h.reader.Get(ctx, cur.ParentID)
	if err != nil {
		return
	}
	if parent.Status != types.StatusWaiting {
		return
	}
	// Check all WaitingFor tasks
	allTerminal := true
	for _, wid := range parent.WaitingFor {
		w, werr := h.reader.Get(ctx, wid)
		if werr != nil || !w.Status.IsTerminal() {
			allTerminal = false
			break
		}
	}
	if !allTerminal {
		return
	}
	if err := h.writer.Resume(ctx, cur.ParentID); err != nil {
		slog.ErrorContext(ctx, "onterminal.resume_failed",
			slog.String("parent_id", string(cur.ParentID)),
			slog.String("err", err.Error()),
		)
		return
	}
	_ = h.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:  fmt.Sprintf("notify:%s:woken", string(cur.ParentID)),
		Kind:   msgbus.KindNotify,
		To:     msgbus.AddrAgent(string(cur.ParentID)),
		TaskID: string(cur.ParentID),
		Payload: msgbus.NotifyPayload{
			Event:  msgbus.NotifyWoken,
			TaskID: string(cur.ParentID),
		},
	})
}
