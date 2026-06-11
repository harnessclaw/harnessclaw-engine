// Package middleware provides the middleware chain for message processing.
package middleware

import (
	"context"

	"harnessclaw-go/pkg/types"
)

// Handler processes an incoming message.
type Handler func(ctx context.Context, msg *types.IncomingMessage) error

// Middleware wraps a handler to add cross-cutting behavior.
type Middleware func(next Handler) Handler

// Chain composes multiple middlewares into a single middleware.
// Middlewares are applied in order: first middleware is the outermost.
func Chain(middlewares ...Middleware) Middleware {
	return func(final Handler) Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}
