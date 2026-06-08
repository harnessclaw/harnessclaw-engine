// Package router handles message routing from channels to the engine.
package router

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/legacy/multimodal"
	"harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/services/api/router/middleware"
	"harnessclaw-go/internal/tools"
	pkgerr "harnessclaw-go/pkg/errors"
	"harnessclaw-go/pkg/types"
)

// ModelInfoProvider exposes the active model + its SupportsFlags so the
// router can run the multimodal Gate before dispatching to the engine.
//
// The interface is intentionally tiny so tests can fake it with a
// struct literal. Production wires a bridge in cmd/server that pulls
// from provider.Manager.ActiveModelKey + registry.LookupModel.
//
// nil ModelInfoProvider disables gating — used by older tests and
// channels that don't have a model registry handy. Production builds
// MUST wire one.
type ModelInfoProvider interface {
	ActiveModelKey() string
	ActiveSupports() registry.SupportsFlags
}

// Router receives standardized messages, runs them through the
// middleware chain, dispatches to the engine, and forwards streaming
// events back to the originating channel.
type Router struct {
	engine    engine.Engine
	channels  map[string]channel.Duplex // channel registry keyed by name
	modelInfo ModelInfoProvider          // optional; nil disables gating
	handler   middleware.Handler
	logger    *zap.Logger
}

// New creates a router with the given engine, channel registry,
// middleware chain, and optional model-info provider for capability
// gating. Pass nil for modelInfo when gating isn't desired (tests).
func New(eng engine.Engine, channels map[string]channel.Duplex, middlewares []middleware.Middleware, modelInfo ModelInfoProvider, logger *zap.Logger) *Router {
	r := &Router{
		engine:    eng,
		channels:  channels,
		modelInfo: modelInfo,
		logger:    logger,
	}

	// Build the middleware chain around the core handler.
	chain := middleware.Chain(middlewares...)
	r.handler = chain(r.coreHandler)

	return r
}

// Handle processes an incoming message through the middleware chain.
func (r *Router) Handle(ctx context.Context, msg *types.IncomingMessage) error {
	return r.handler(ctx, msg)
}

// coreHandler dispatches to the engine and forwards events to the channel.
//
// IMPORTANT: For user.message, event forwarding runs in a background goroutine
// so that the readPump is NOT blocked. This allows the readPump to continue
// reading tool.result, permission.response, and session.interrupt messages
// while the query loop is still running.
func (r *Router) coreHandler(ctx context.Context, msg *types.IncomingMessage) error {
	// If this is a tool.result from the client, forward to the engine directly.
	if msg.ToolResult != nil {
		return r.engine.SubmitToolResult(ctx, msg.SessionID, msg.ToolResult)
	}

	// If this is a permission.response from the client, forward to the engine.
	if msg.PermissionResponse != nil {
		return r.engine.SubmitPermissionResult(ctx, msg.SessionID, msg.PermissionResponse)
	}

	// If this is a plan.response from the client, forward to the engine
	// so the awaiting PlanCoordinator unblocks. Doesn't go through the
	// query loop — it resolves an in-flight async request, not start
	// a new one.
	if msg.PlanResponse != nil {
		return r.engine.SubmitPlanResponse(ctx, msg.SessionID, msg.PlanResponse)
	}

	// step.decision.response from the client — same async-resolve path
	// as plan.response. Unblocks the Scheduler / PlanCoordinator that's
	// pausing on a hard step or plan-level failure.
	if msg.StepDecisionResponse != nil {
		return r.engine.SubmitStepDecision(ctx, msg.SessionID, msg.StepDecisionResponse)
	}

	// Normalize wire content blocks into engine-internal ContentBlock[].
	// Falls back to the legacy text-only path when msg.Content is empty.
	blocks, err := multimodal.Build(msg.Text, msg.Content)
	if err != nil {
		r.logger.Warn("router: multimodal build failed",
			zap.String("session_id", msg.SessionID),
			zap.Error(err),
		)
		r.emitInvalidInput(ctx, msg, err)
		return err
	}

	// Capability gate. Only runs when a ModelInfoProvider is wired in;
	// nil means "trust whatever the adapter receives" (used by tests
	// that don't care about gating).
	if r.modelInfo != nil {
		if gateErr := multimodal.Gate(r.modelInfo.ActiveModelKey(), r.modelInfo.ActiveSupports(), blocks); gateErr != nil {
			r.logger.Info("router: multimodal gate rejected message",
				zap.String("session_id", msg.SessionID),
				zap.Error(gateErr),
			)
			r.emitUnsupportedModality(ctx, msg, gateErr)
			return gateErr
		}
	}

	userMsg := &types.Message{
		Role:    types.RoleUser,
		Content: blocks,
	}

	// L2 coordinator mode override (per-turn). Empty string means
	// "auto-decide via ModeSelector"; any other value flows through to
	// SpawnConfig.CoordinatorMode → resolveCoordinator → registry.
	// Validation lives downstream — unknown modes degrade to ReAct
	// with a warn log rather than crashing the request.
	if msg.CoordinatorMode != "" {
		ctx = tool.WithCoordinatorMode(ctx, msg.CoordinatorMode)
		r.logger.Info("router: coordinator_mode override applied",
			zap.String("session_id", msg.SessionID),
			zap.String("coordinator_mode", msg.CoordinatorMode),
		)
	}

	// Plan confirmation opt-in (only meaningful in plan mode). When
	// "required", PlanCoordinator pauses for user review via the
	// plan.proposed / plan.response round-trip.
	if msg.PlanConfirmation != "" {
		ctx = tool.WithPlanConfirmation(ctx, msg.PlanConfirmation)
		r.logger.Info("router: plan_confirmation applied",
			zap.String("session_id", msg.SessionID),
			zap.String("plan_confirmation", msg.PlanConfirmation),
		)
	}

	events, err := r.engine.ProcessMessage(ctx, msg.SessionID, userMsg)
	if err != nil {
		r.logger.Error("engine processing failed",
			zap.String("session_id", msg.SessionID),
			zap.Error(err),
		)
		return err
	}

	// Look up the originating channel to forward streaming events.
	ch, ok := r.channels[msg.ChannelName]
	if !ok {
		// Drain to avoid blocking the engine goroutine, then return error.
		for range events {
		}
		return pkgerr.New(pkgerr.CodeNotFound, "channel not found: "+msg.ChannelName)
	}

	// Forward engine events in a background goroutine so the caller (readPump)
	// is free to read subsequent client messages (tool.result, permission.response,
	// session.interrupt) while the query loop is still running. Without this,
	// the readPump would be blocked until the entire query loop finishes, creating
	// a deadlock for any protocol that requires mid-query client→server messages.
	go func() {
		if sendErr := ch.Reply(ctx, msg.SessionID, channel.Outbound{Stream: events}); sendErr != nil {
			r.logger.Error("failed to reply to channel",
				zap.String("channel", msg.ChannelName),
				zap.String("session_id", msg.SessionID),
				zap.Error(sendErr),
			)
		}
	}()

	return nil
}

// emitInvalidInput sends a single-shot error frame to the originating
// channel when multimodal.Build rejects the incoming content (malformed
// block, oversize, unknown type). No engine round-trip happens.
func (r *Router) emitInvalidInput(ctx context.Context, msg *types.IncomingMessage, cause error) {
	ch, ok := r.channels[msg.ChannelName]
	if !ok {
		return
	}
	ev := types.EngineEvent{
		Type:  types.EngineEventError,
		Error: cause,
		Terminal: &types.Terminal{
			Reason:  types.TerminalModelError,
			Message: cause.Error(),
		},
	}
	_ = ch.Reply(ctx, msg.SessionID, channel.Outbound{Stream: singleEvent(ev)})
}

// emitUnsupportedModality sends an error frame carrying the rich
// UnsupportedModalityError payload (user-facing message, rejected
// modality list, model key). Channel translator turns the
// ErrorDetails map into typed wire fields (user_message / code /
// details).
func (r *Router) emitUnsupportedModality(ctx context.Context, msg *types.IncomingMessage, cause error) {
	ch, ok := r.channels[msg.ChannelName]
	if !ok {
		return
	}
	ev := types.EngineEvent{
		Type:  types.EngineEventError,
		Error: cause,
		Terminal: &types.Terminal{
			Reason:  types.TerminalUnsupportedModality,
			Message: cause.Error(),
		},
	}
	if u, ok := cause.(*multimodal.UnsupportedModalityError); ok {
		ev.ErrorDetails = map[string]any{
			"model":               u.Model,
			"rejected_modalities": u.RejectedModalities,
			"user_message":        u.UserMessage(),
			"error_code":          "model_lacks_modality",
		}
	}
	_ = ch.Reply(ctx, msg.SessionID, channel.Outbound{Stream: singleEvent(ev)})
}

// singleEvent wraps one EngineEvent into a 1-element read-only channel,
// used by emitInvalidInput / emitUnsupportedModality where there is no
// engine round-trip — just a single error frame to ship out.
func singleEvent(ev types.EngineEvent) <-chan types.EngineEvent {
	ch := make(chan types.EngineEvent, 1)
	ch <- ev
	close(ch)
	return ch
}
