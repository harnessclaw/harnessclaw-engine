// Package audit logs scheduler-internal events that don't fit the regular
// debug log: admit rejections, dropped stale events, saga rollbacks.
package audit

import (
	"context"
	"log/slog"
)

type Logger interface {
	Log(ctx context.Context, event string, attrs ...slog.Attr)
}

// NewSlogLogger wraps an existing *slog.Logger (typically the service-wide one
// passed in via scheduler.New). Adds module=audit on every record.
func NewSlogLogger(l *slog.Logger) Logger {
	return &slogLogger{l: l.With(slog.String("module", "audit"))}
}

type slogLogger struct{ l *slog.Logger }

func (s *slogLogger) Log(ctx context.Context, event string, attrs ...slog.Attr) {
	s.l.LogAttrs(ctx, slog.LevelWarn, event, attrs...)
}
