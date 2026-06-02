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
	// Synthetic summary must be carried as a USER message — Anthropic
	// requires the first non-system message to be role=user, and the
	// summary always lands at index 0 here. Was previously assistant
	// (legacy) which caused 400 when an original leading user turn got
	// summarized away.
	if result[0].Role != types.RoleUser {
		t.Errorf("summary message role = %q, want %q", result[0].Role, types.RoleUser)
	}
	// Text must carry both the framing prefix (so the model reads it
	// as injected context, not a real user turn) and the LLM summary.
	want := "[Prior conversation summary]\nconversation summary here"
	if got := result[0].Content[0].Text; got != want {
		t.Errorf("first message text = %q, want %q", got, want)
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

// Regression: microCompact must not leave an orphan tool_result at the head
// of the kept tail. OpenAI rejects a "tool" role message that isn't preceded
// by an assistant with matching tool_calls; the bifrost adapter converts any
// user message containing ContentTypeToolResult into role=tool.
//
// Construct an alternating assistant_with_tool_use / user_tool_result chain
// where the naive midpoint lands on a tool_result whose matching tool_use
// would be discarded.
func TestMicroCompact_SkipsOrphanToolResultAtBoundary(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "task"}}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ToolUseID: "a"}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "a"}}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ToolUseID: "b"}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "b"}}}, // naive keepFrom (5/2=... — see 9-msg version below)
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ToolUseID: "c"}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "c"}}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ToolUseID: "d"}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "d"}}},
	}
	// Naive keepFrom = 9/2 = 4 → msgs[4] = tool_result B (orphan, because
	// msgs[3] = its tool_use is in the discarded prefix). After fix,
	// keepFrom advances to 5 (assistant tool_use C).
	result := c.microCompact(msgs)
	if len(result) < 2 {
		t.Fatalf("expected kept head + tail, got %d messages", len(result))
	}
	// First kept message after msgs[0] must NOT be a tool_result.
	if containsToolResult(result[1]) {
		t.Errorf("kept tail starts with orphan tool_result: %+v", result[1])
	}
	// Specifically, expect the advance to land on assistant C.
	if result[1].Content[0].Type != types.ContentTypeToolUse || result[1].Content[0].ToolUseID != "c" {
		t.Errorf("expected kept tail to start at tool_use C, got %+v", result[1])
	}
}

// Regression: full Compact must apply the same orphan-tool_result guard at
// its 2/3 boundary.
func TestCompact_SkipsOrphanToolResultAtBoundary(t *testing.T) {
	mp := &mockProvider{summary: "summary"}
	c := NewLLMCompactor(mp, testLogger())
	// 12 messages, naive keepFrom = 12*2/3 = 8. Place a tool_result at
	// index 8 whose tool_use (index 7) is in the summarized prefix.
	msgs := make([]types.Message, 12)
	for i := range msgs {
		msgs[i] = types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "m"}},
		}
	}
	msgs[7] = types.Message{
		Role:    types.RoleAssistant,
		Content: []types.ContentBlock{{Type: types.ContentTypeToolUse, ToolUseID: "x"}},
	}
	msgs[8] = types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolUseID: "x"}},
	}
	result, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) < 2 {
		t.Fatalf("expected summary + tail, got %d messages", len(result))
	}
	// result[0] is summary assistant; result[1] is the head of the kept
	// tail and must not be an orphan tool_result.
	if containsToolResult(result[1]) {
		t.Errorf("kept tail starts with orphan tool_result: %+v", result[1])
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
