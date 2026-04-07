package middleware

import (
	"context"
	"time"

	"harnessclaw-go/pkg/types"
	"go.uber.org/zap"
)

// Logging creates a middleware that logs each incoming message with duration.
func Logging(logger *zap.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, msg *types.IncomingMessage) error {
			start := time.Now()
			logger.Info("message received",
				zap.String("channel", msg.ChannelName),
				zap.String("session_id", msg.SessionID),
				zap.String("user_id", msg.UserID),
			)

			err := next(ctx, msg)

			duration := time.Since(start)
			if err != nil {
				logger.Error("message processing failed",
					zap.String("session_id", msg.SessionID),
					zap.Duration("duration", duration),
					zap.Error(err),
				)
			} else {
				logger.Info("message processed",
					zap.String("session_id", msg.SessionID),
					zap.Duration("duration", duration),
				)
			}
			return err
		}
	}
}
