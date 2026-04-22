package engine

import (
	"context"
	"fmt"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

type retryMockProvider struct {
	calls     int
	failUntil int // fail the first N calls
	failErr   error
}

func (p *retryMockProvider) Name() string { return "retry-mock" }

func (p *retryMockProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	p.calls++
	if p.calls <= p.failUntil {
		return nil, p.failErr
	}
	// Success: return a simple stream.
	ch := make(chan types.StreamEvent, 3)
	go func() {
		defer close(ch)
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "ok"}
		ch <- types.StreamEvent{
			Type:       types.StreamEventMessageEnd,
			StopReason: "end_turn",
			Usage:      &types.Usage{InputTokens: 10, OutputTokens: 5},
		}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func (p *retryMockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 100, nil
}

func TestRetryLLMCall_SuccessOnFirstAttempt(t *testing.T) {
	prov := &retryMockProvider{}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), nil)

	if result.streamErr != nil {
		t.Fatalf("unexpected error: %v", result.streamErr)
	}
	if result.textBuf != "ok" {
		t.Errorf("expected text 'ok', got %q", result.textBuf)
	}
	if prov.calls != 1 {
		t.Errorf("expected 1 call, got %d", prov.calls)
	}
}

func TestRetryLLMCall_SuccessAfterRetry(t *testing.T) {
	prov := &retryMockProvider{
		failUntil: 2,
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), nil)

	if result.streamErr != nil {
		t.Fatalf("unexpected error: %v", result.streamErr)
	}
	if result.textBuf != "ok" {
		t.Errorf("expected text 'ok', got %q", result.textBuf)
	}
	if prov.calls != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", prov.calls)
	}
}

func TestRetryLLMCall_ExhaustsRetries(t *testing.T) {
	prov := &retryMockProvider{
		failUntil: 10, // more than max retries
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), nil)

	if result.streamErr == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// 1 initial + 3 retries = 4 calls
	if prov.calls != llmMaxRetries+1 {
		t.Errorf("expected %d calls, got %d", llmMaxRetries+1, prov.calls)
	}
}

func TestRetryLLMCall_NonRetryableError(t *testing.T) {
	prov := &retryMockProvider{
		failUntil: 10,
		failErr:   fmt.Errorf("auth failed: 401 Unauthorized"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), nil)

	if result.streamErr == nil {
		t.Fatal("expected error for non-retryable failure")
	}
	// Should not retry auth errors.
	if prov.calls != 1 {
		t.Errorf("expected 1 call (no retry for auth errors), got %d", prov.calls)
	}
}

func TestRetryLLMCall_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	prov := &retryMockProvider{
		failUntil: 10,
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(ctx, prov, &provider.ChatRequest{}, zap.NewNop(), nil)

	if result.streamErr == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		err       string
		retryable bool
	}{
		{"read tcp: i/o timeout", true},
		{"connection reset by peer", true},
		{"unexpected EOF", true},
		{"bifrost stream error: 529 overloaded", true},
		{"bifrost: stream request failed: 500 Internal Server Error", true},
		{"auth failed: 401 Unauthorized", false},
		{"prompt too long", false},
		{"unknown error foobar", false},
	}
	for _, tt := range tests {
		got := isRetryableError(fmt.Errorf("%s", tt.err))
		if got != tt.retryable {
			t.Errorf("isRetryableError(%q) = %v, want %v", tt.err, got, tt.retryable)
		}
	}
}
