// Package router handles message routing from channels to the engine.
package router

import (
	"context"

	"go.uber.org/zap"
	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/router/middleware"
	pkgerr "harnessclaw-go/pkg/errors"
	"harnessclaw-go/pkg/types"
)

// Router receives standardized messages, runs them through the middleware
// chain, dispatches to the engine, and forwards streaming events back to
// the originating channel.
type Router struct {
	engine   engine.Engine
	channels map[string]channel.Channel // channel registry keyed by name
	handler  middleware.Handler
	logger   *zap.Logger
}

// New creates a router with the given engine, channel registry, and middleware chain.
func New(eng engine.Engine, channels map[string]channel.Channel, middlewares []middleware.Middleware, logger *zap.Logger) *Router {
	r := &Router{
		engine:   eng,
		channels: channels,
		logger:   logger,
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

	userMsg := &types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: msg.Text},
		},
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
		for evt := range events {
			if sendErr := ch.SendEvent(ctx, msg.SessionID, &evt); sendErr != nil {
				r.logger.Error("failed to send event to channel",
					zap.String("channel", msg.ChannelName),
					zap.String("session_id", msg.SessionID),
					zap.Error(sendErr),
				)
			}
		}
	}()

	return nil
}
