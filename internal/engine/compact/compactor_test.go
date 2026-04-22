package compact

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/artifact"
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
	// First message should be the summary.
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

// --- Artifact-aware compaction tests ---

func TestReplaceArtifactContent_NoStore(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	// No artifact store set.
	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolResult: "big content", ArtifactID: "art_123"},
			},
		},
	}
	result := c.replaceArtifactContent(msgs)
	// Should return unchanged messages.
	if result[0].Content[0].ToolResult != "big content" {
		t.Errorf("content should be unchanged without store, got %q", result[0].Content[0].ToolResult)
	}
}

func TestReplaceArtifactContent_WithStore(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	store := artifact.NewStore()
	artID := store.Save("tu_1", "Read", "very large content that should be replaced", nil)
	c.SetArtifactStore(store)

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolResult: "very large content that should be replaced", ArtifactID: artID},
			},
		},
	}

	result := c.replaceArtifactContent(msgs)
	replaced := result[0].Content[0].ToolResult
	if replaced == "very large content that should be replaced" {
		t.Error("content should have been replaced with artifact reference")
	}
	if !strings.Contains(replaced, artID) {
		t.Errorf("replacement should contain artifact ID %q, got %q", artID, replaced)
	}
}

func TestReplaceArtifactContent_NoArtifactID(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	store := artifact.NewStore()
	c.SetArtifactStore(store)

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolResult: "small content"},
			},
		},
	}

	result := c.replaceArtifactContent(msgs)
	if result[0].Content[0].ToolResult != "small content" {
		t.Error("content without artifact_id should not be replaced")
	}
}

func TestReplaceArtifactContent_MixedBlocks(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	store := artifact.NewStore()
	artID := store.Save("tu_1", "Read", "artifact content", nil)
	c.SetArtifactStore(store)

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "some text"},
				{Type: types.ContentTypeToolResult, ToolResult: "artifact content", ArtifactID: artID},
				{Type: types.ContentTypeToolResult, ToolResult: "inline content"},
			},
		},
	}

	result := c.replaceArtifactContent(msgs)
	// Text block unchanged.
	if result[0].Content[0].Text != "some text" {
		t.Error("text block should be unchanged")
	}
	// First tool result replaced.
	if result[0].Content[1].ToolResult == "artifact content" {
		t.Error("tool result with artifact_id should be replaced")
	}
	// Second tool result unchanged.
	if result[0].Content[2].ToolResult != "inline content" {
		t.Error("tool result without artifact_id should be unchanged")
	}
}

func TestReplaceArtifactContent_DoesNotMutateOriginal(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	store := artifact.NewStore()
	artID := store.Save("tu_1", "Read", "original", nil)
	c.SetArtifactStore(store)

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolResult: "original", ArtifactID: artID},
			},
		},
	}

	_ = c.replaceArtifactContent(msgs)
	// Original should be untouched.
	if msgs[0].Content[0].ToolResult != "original" {
		t.Error("original message should not be mutated")
	}
}

func TestCompact_ArtifactAwareSummarization(t *testing.T) {
	mp := &mockProvider{summary: "summary with artifact refs"}
	c := NewLLMCompactor(mp, testLogger())
	store := artifact.NewStore()
	artID := store.Save("tu_1", "Read", strings.Repeat("x", 5000), nil)
	c.SetArtifactStore(store)

	msgs := make([]types.Message, 12)
	msgs[0] = types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolResult: strings.Repeat("x", 5000), ArtifactID: artID},
		},
	}
	for i := 1; i < 12; i++ {
		msgs[i] = types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "msg"}},
		}
	}

	result, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have summary + 4 recent messages.
	if len(result) != 5 {
		t.Errorf("expected 5 messages, got %d", len(result))
	}
	if result[0].Content[0].Text != "summary with artifact refs" {
		t.Errorf("summary = %q", result[0].Content[0].Text)
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

// --- SetArtifactStore ---

func TestSetArtifactStore(t *testing.T) {
	c := NewLLMCompactor(&mockProvider{}, testLogger())
	if c.artifactStore != nil {
		t.Error("artifact store should be nil initially")
	}
	store := artifact.NewStore()
	c.SetArtifactStore(store)
	if c.artifactStore == nil {
		t.Error("artifact store should be set after SetArtifactStore")
	}
}
