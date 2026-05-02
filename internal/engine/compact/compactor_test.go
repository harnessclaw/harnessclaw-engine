package compact

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	summary string
	err     error
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func (m *mockProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	if m.err != nil {
		return nil, m.err
	}
	events := make(chan types.StreamEvent, 2)
	events <- types.StreamEvent{Type: types.StreamEventText, Text: m.summary}
	close(events)
	return &provider.ChatStream{
		Events: events,
		Err:    func() error { return nil },
	}, nil
}

func (m *mockProvider) SupportsImages() bool { return false }

func testLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

// --- ShouldCompact tests ---

func TestShouldCompact_BelowThreshold(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := []types.Message{
		{Tokens: 100},
		{Tokens: 200},
	}
	if c.ShouldCompact(msgs, 1000, 0.8) {
		t.Error("should not compact when below threshold (300 < 800)")
	}
}

func TestShouldCompact_AboveThreshold(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := []types.Message{
		{Tokens: 500},
		{Tokens: 400},
	}
	if !c.ShouldCompact(msgs, 1000, 0.8) {
		t.Error("should compact when above threshold (900 > 800)")
	}
}

func TestShouldCompact_CircuitBreakerOpen(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	c.failureCount.Store(3) // trip the circuit breaker
	msgs := []types.Message{
		{Tokens: 900},
	}
	if c.ShouldCompact(msgs, 1000, 0.8) {
		t.Error("should not compact when circuit breaker is open")
	}
}

// --- Compact tests ---

func TestCompact_TooFewMessages(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := []types.Message{
		{Role: types.RoleUser},
		{Role: types.RoleAssistant},
	}
	result, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 messages unchanged, got %d", len(result))
	}
}

func TestCompact_MicroCompact(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := make([]types.Message, 8)
	for i := range msgs {
		msgs[i] = types.Message{Role: types.RoleUser}
	}
	result, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// microCompact keeps first + last half: 1 + 4 = 5
	if len(result) != 5 {
		t.Errorf("microCompact: expected 5 messages, got %d", len(result))
	}
}

func TestCompact_FullCompact(t *testing.T) {
	mp := &mockProvider{summary: "conversation summary here"}
	c := NewLLMCompactor(mp, testLogger())

	msgs := make([]types.Message, 12)
	for i := range msgs {
		msgs[i] = types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "message"},
			},
		}
	}

	result, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 summary + 4 recent (12 * 1/3 = 4)
	if len(result) != 5 {
		t.Errorf("expected 5 messages after compact, got %d", len(result))
	}
	if result[0].Content[0].Text != "conversation summary here" {
		t.Errorf("first message should be summary, got %q", result[0].Content[0].Text)
	}
}

func TestCompact_CircuitBreakerOnFailure(t *testing.T) {
	mp := &mockProvider{err: context.DeadlineExceeded}
	c := NewLLMCompactor(mp, testLogger())

	msgs := make([]types.Message, 12)
	for i := range msgs {
		msgs[i] = types.Message{Role: types.RoleUser}
	}

	_, err := c.Compact(context.Background(), msgs)
	if err == nil {
		t.Error("expected error from failed summarization")
	}
	if c.failureCount.Load() != 1 {
		t.Errorf("failure count = %d, want 1", c.failureCount.Load())
	}
}

func TestCompact_CircuitBreakerResetsOnSuccess(t *testing.T) {
	mp := &mockProvider{summary: "summary"}
	c := NewLLMCompactor(mp, testLogger())
	c.failureCount.Store(2) // near threshold

	msgs := make([]types.Message, 12)
	for i := range msgs {
		msgs[i] = types.Message{Role: types.RoleUser}
	}

	_, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.failureCount.Load() != 0 {
		t.Errorf("failure count should reset to 0, got %d", c.failureCount.Load())
	}
}

// --- microCompact tests ---

func TestMicroCompact_TwoMessages(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := []types.Message{
		{Role: types.RoleUser},
		{Role: types.RoleAssistant},
	}
	result := c.microCompact(msgs)
	if len(result) != 2 {
		t.Errorf("expected 2 messages unchanged, got %d", len(result))
	}
}

func TestMicroCompact_SixMessages(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := make([]types.Message, 6)
	for i := range msgs {
		msgs[i] = types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: string(rune('A' + i))},
		}}
	}
	result := c.microCompact(msgs)
	// first (A) + last 3 (D, E, F) = 4
	if len(result) != 4 {
		t.Errorf("expected 4 messages, got %d", len(result))
	}
	if result[0].Content[0].Text != "A" {
		t.Errorf("first message should be A, got %q", result[0].Content[0].Text)
	}
}
