package engine

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"harnessclaw-go/internal/engine/llmcall"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// TestLLMCallOk_LogsTokenBreakdown verifies that the "llm.call ok" INFO log
// line carries all five token-usage fields from the provider's MessageEnd event.
// Before the fix these fields are absent; after they must equal the exact
// values emitted by the fake provider.
func TestLLMCallOk_LogsTokenBreakdown(t *testing.T) {
	// Build an observer-backed zap.Logger so we can inspect emitted fields.
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)

	// fakeProv emits one text chunk followed by a MessageEnd carrying
	// the specific usage we want to assert on.
	prov := &engineFakeProv{
		events: []types.StreamEvent{
			{Type: types.StreamEventText, Text: "hello"},
			{
				Type:       types.StreamEventMessageEnd,
				StopReason: "end_turn",
				Usage: &types.Usage{
					InputTokens:    1234,
					OutputTokens:   56,
					CacheRead:      100,
					CacheWrite:     7,
					ThinkingTokens: 2,
				},
			},
		},
	}

	out := make(chan types.EngineEvent, 16)
	// Drain the output channel in a goroutine so CallLLM never blocks.
	go func() {
		for range out {
		}
	}()

	result := llmcall.CallLLM(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		logger,
		nil, // nil retryer = single attempt, no retries
		llmcall.LLMCallTimeouts{},
		"agent_test",
		out,
		nil,
	)
	close(out)

	if result.StreamErr != nil {
		t.Fatalf("CallLLM returned unexpected error: %v", result.StreamErr)
	}

	// Find the single "llm.call ok" log entry.
	entries := logs.FilterMessage("llm.call ok").All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 'llm.call ok' log entry, got %d", len(entries))
	}
	entry := entries[0]

	// Build a helper that extracts a named int64 field value.
	fieldVal := func(name string) (int64, bool) {
		for _, f := range entry.Context {
			if f.Key == name {
				return f.Integer, true
			}
		}
		return 0, false
	}

	cases := []struct {
		field string
		want  int64
	}{
		{"input_tokens", 1234},
		{"output_tokens", 56},
		{"cache_read", 100},
		{"cache_write", 7},
		{"thinking_tokens", 2},
	}

	for _, tc := range cases {
		val, ok := fieldVal(tc.field)
		if !ok {
			t.Errorf("missing field %s on 'llm.call ok' log line", tc.field)
			continue
		}
		if val != tc.want {
			t.Errorf("field %s = %d, want %d", tc.field, val, tc.want)
		}
	}
}
