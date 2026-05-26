package host

import (
	"context"
	"fmt"
	"time"

	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// Reaper scans running and cancelling tasks for three expiry conditions (spec §5.7):
//  1. Lease expired         → notify{lease_expired}        → onExpire → FailOrRetry
//  2. Deadline exceeded     → notify{deadline_exceeded}    → onExpire → FailOrRetry
//  3. Cancelling + drained  → notify{cancelling_drained}   → onCancellingDrained → ConfirmCancelled
//
// The Reaper publishes to AddrScheduler; From is AddrReaper per spec.
// It never mutates TaskState directly — all mutations happen via the handler chain.
type Reaper struct {
	kernel tstate.Reader
	bus    msgbus.Bus
}

// NewReaper constructs a Reaper.
func NewReaper(kernel tstate.Reader, bus msgbus.Bus) *Reaper {
	return &Reaper{kernel: kernel, bus: bus}
}

// Run starts the periodic scan loop. Blocks until ctx is cancelled.
// Returns ctx.Err() on cancellation.
func (r *Reaper) Run(ctx context.Context, interval time.Duration) error {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			r.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single 3-pass scan. Safe to call from tests.
func (r *Reaper) RunOnce(ctx context.Context) {
	r.scanRunning(ctx)
	r.scanCancelling(ctx)
}

// scanRunning checks all running tasks for lease expiry and deadline exceeded.
func (r *Reaper) scanRunning(ctx context.Context) {
	tasks, err := r.kernel.ListByStatus(ctx, "", types.StatusRunning, 0)
	if err != nil {
		return
	}
	now := time.Now()
	for _, ts := range tasks {
		// Pass 1: lease expired
		if !ts.Lease.ExpiresAt.IsZero() && now.After(ts.Lease.ExpiresAt) {
			_ = r.publish(ctx, ts.ID, msgbus.NotifyLeaseExpired,
				fmt.Sprintf("reaper:lease:%s", ts.ID))
			continue // don't double-fire deadline on same task this scan
		}
		// Pass 2: absolute deadline exceeded
		if !ts.LeafSpec.Budget.Deadline.IsZero() && now.After(ts.LeafSpec.Budget.Deadline) {
			_ = r.publish(ctx, ts.ID, msgbus.NotifyDeadlineExceeded,
				fmt.Sprintf("reaper:deadline:%s", ts.ID))
		}
	}
}

// scanCancelling checks all cancelling tasks; if no children are still running,
// publishes cancelling_drained so onCancellingDrained can finalize.
func (r *Reaper) scanCancelling(ctx context.Context) {
	tasks, err := r.kernel.ListByStatus(ctx, "", types.StatusCancelling, 0)
	if err != nil {
		return
	}
	for _, ts := range tasks {
		children, err := r.kernel.ListChildren(ctx, ts.ID)
		if err != nil {
			continue
		}
		drained := true
		for _, c := range children {
			if !c.Status.IsTerminal() {
				drained = false
				break
			}
		}
		if drained {
			_ = r.publish(ctx, ts.ID, msgbus.NotifyCancellingDrained,
				fmt.Sprintf("reaper:drained:%s", ts.ID))
		}
	}
}

func (r *Reaper) publish(ctx context.Context, id types.TaskID, event msgbus.NotifyEvent, msgID string) error {
	return r.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:  msgID,
		Kind:   msgbus.KindNotify,
		From:   msgbus.AddrReaper,
		To:     msgbus.AddrScheduler,
		TaskID: string(id),
		Payload: msgbus.NotifyPayload{
			Event:  event,
			TaskID: string(id),
		},
	})
}
