package audit

import (
	"context"
	"log/slog"
	"testing"
)

func TestNewSlogLogger(t *testing.T) {
	// Test that NewSlogLogger works with a default slog logger
	baseLogger := slog.Default()
	logger := NewSlogLogger(baseLogger)

	if logger == nil {
		t.Fatal("NewSlogLogger returned nil")
	}

	// Test that Log doesn't panic with valid inputs
	ctx := context.Background()
	logger.Log(ctx, "test_event", slog.String("key", "value"))
}

func TestSlogLoggerInterface(t *testing.T) {
	// Verify the Logger interface is satisfied
	var _ Logger = NewSlogLogger(slog.Default())
}
