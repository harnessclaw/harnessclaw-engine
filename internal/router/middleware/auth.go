package middleware

import (
	"context"

	"harnessclaw-go/pkg/errors"
	"harnessclaw-go/pkg/types"
)

// Auth creates an authentication middleware.
// The validator function returns true if the message is authenticated.
func Auth(validator func(ctx context.Context, msg *types.IncomingMessage) bool) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, msg *types.IncomingMessage) error {
			if !validator(ctx, msg) {
				return errors.New(errors.CodePermissionDenied, "authentication failed")
			}
			return next(ctx, msg)
		}
	}
}
