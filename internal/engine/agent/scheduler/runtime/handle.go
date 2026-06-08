// Package runtime wires handlers, router, and the reaper into a running scheduler loop.
package runtime

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/agent/scheduler/audit"
	"harnessclaw-go/internal/engine/agent/scheduler/handlers"
	"harnessclaw-go/internal/engine/agent/scheduler/router"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// Handle creates and wires all handlers into a Router, then runs the router
// loop until ctx is cancelled or the bus closes.
//
// Routing table:
//
//	KindLifecycle                          → onLifecycle
//	KindControl  (cmd=spawn)               → onSpawn
//	KindNotify   (lease_expired|deadline)  → onExpire
//	KindNotify   (completed_from_staging)  → onCompletedFromStaging
//	KindNotify   (cancelling_drained)      → onCancellingDrained
//	KindNotify   (succeeded|failed|cancelled|woken) → onTerminal
//	KindResult                             → onResult
//
// If ready is non-nil it is closed after the bus subscription is established.
func Handle(ctx context.Context, kernel tstate.Kernel, bus msgbus.Bus, log audit.Logger, addr msgbus.Address, ready chan<- struct{}) error {
	r := router.New(kernel, bus, log)

	// --- build handlers ---
	onLifecycle := handlers.NewOnLifecycle(kernel, kernel, bus, log)
	onSpawn := handlers.NewOnSpawn(kernel, kernel, bus)
	onExpire := handlers.NewOnExpire(kernel, kernel, bus)
	onCompletedFromStaging := handlers.NewOnCompletedFromStaging(kernel, kernel, bus)
	onCancellingDrained := handlers.NewOnCancellingDrained(kernel, kernel, bus)
	onTerminal := handlers.NewOnTerminal(kernel, kernel, bus)
	onResult := handlers.NewOnResult()

	// --- KindLifecycle ---
	r.Handle(msgbus.KindLifecycle, func(ctx context.Context, msg msgbus.AgentMessage) error {
		onLifecycle.Handle(ctx, msg)
		return nil
	})

	// --- KindControl: route only CmdSpawn ---
	r.Handle(msgbus.KindControl, func(ctx context.Context, msg msgbus.AgentMessage) error {
		p, ok := msg.Payload.(msgbus.ControlPayload)
		if !ok {
			return nil
		}
		if p.Cmd == msgbus.CmdSpawn {
			onSpawn.Handle(ctx, msg)
		}
		return nil
	})

	// --- KindResult ---
	r.Handle(msgbus.KindResult, func(ctx context.Context, msg msgbus.AgentMessage) error {
		onResult.Handle(ctx, msg)
		return nil
	})

	// --- KindNotify: fan-out by event ---
	r.Handle(msgbus.KindNotify, func(ctx context.Context, msg msgbus.AgentMessage) error {
		p, ok := msg.Payload.(msgbus.NotifyPayload)
		if !ok {
			return fmt.Errorf("runtime: KindNotify bad payload type %T", msg.Payload)
		}
		switch p.Event {
		case msgbus.NotifyLeaseExpired, msgbus.NotifyDeadlineExceeded:
			onExpire.Handle(ctx, msg)
		case msgbus.NotifyCompletedFromStaging:
			onCompletedFromStaging.Handle(ctx, msg)
		case msgbus.NotifyCancellingDrained:
			onCancellingDrained.Handle(ctx, msg)
		case msgbus.NotifySucceeded, msgbus.NotifyFailed, msgbus.NotifyCancelled, msgbus.NotifyWoken:
			onTerminal.Handle(ctx, p.Event, types.TaskID(p.TaskID))
		}
		// NotifyReady, NotifySpawnGranted, NotifySpawnFailed, NotifyWoken — no further action at scheduler level
		return nil
	})

	return r.Run(ctx, addr, ready)
}
