package mock

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

func TestMockProvider_TextResponse(t *testing.T) {
	mp := New(TextResponse("Hello, world!"))

	stream, err := mp.Chat(context.Background(), &provider.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotText string
	var gotStopReason string
	for ev := range stream.Events {
		switch ev.Type {
		case types.StreamEventText:
			gotText += ev.Text
		case types.StreamEventMessageEnd:
			gotStopReason = ev.StopReason
		}
	}

	if gotText != "Hello, world!" {
		t.Errorf("text = %q, want %q", gotText, "Hello, world!")
	}
	if gotStopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want %q", gotStopReason, "end_turn")
	}
	if err := stream.Err(); err != nil {
		t.Errorf("stream.Err() = %v, want nil", err)
	}
}

func TestMockProvider_ToolCallResponse(t *testing.T) {
	toolCalls := []types.ToolCall{
		{ID: "tc_1", Name: "read_file", Input: `{"path":"/tmp/x"}`},
		{ID: "tc_2", Name: "bash", Input: `{"cmd":"ls"}`},
	}

	mp := New(ToolResponse("thinking...", toolCalls...))

	stream, err := mp.Chat(context.Background(), &provider.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}

	// Expect: text, tool_use, tool_use, message_end
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	if events[0].Type != types.StreamEventText {
		t.Errorf("event[0].Type = %q, want %q", events[0].Type, types.StreamEventText)
	}
	if events[0].Text != "thinking..." {
		t.Errorf("event[0].Text = %q, want %q", events[0].Text, "thinking...")
	}

	if events[1].Type != types.StreamEventToolUse {
		t.Errorf("event[1].Type = %q, want %q", events[1].Type, types.StreamEventToolUse)
	}
	if events[1].ToolCall.Name != "read_file" {
		t.Errorf("event[1].ToolCall.Name = %q, want %q", events[1].ToolCall.Name, "read_file")
	}

	if events[2].Type != types.StreamEventToolUse {
		t.Errorf("event[2].Type = %q, want %q", events[2].Type, types.StreamEventToolUse)
	}
	if events[2].ToolCall.Name != "bash" {
		t.Errorf("event[2].ToolCall.Name = %q, want %q", events[2].ToolCall.Name, "bash")
	}

	if events[3].Type != types.StreamEventMessageEnd {
		t.Errorf("event[3].Type = %q, want %q", events[3].Type, types.StreamEventMessageEnd)
	}
	if events[3].StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want %q", events[3].StopReason, "tool_use")
	}
}

func TestMockProvider_MultipleResponses(t *testing.T) {
	mp := New(
		TextResponse("first"),
		TextResponse("second"),
		TextResponse("third"),
	)

	for i, want := range []string{"first", "second", "third"} {
		stream, err := mp.Chat(context.Background(), &provider.ChatRequest{})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}

		var gotText string
		for ev := range stream.Events {
			if ev.Type == types.StreamEventText {
				gotText += ev.Text
			}
		}
		if gotText != want {
			t.Errorf("call %d: text = %q, want %q", i, gotText, want)
		}
	}

	if mp.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0", mp.Remaining())
	}
}

func TestMockProvider_Exhausted(t *testing.T) {
	mp := New(TextResponse("only one"))

	// First call succeeds.
	_, err := mp.Chat(context.Background(), &provider.ChatRequest{})
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	// Second call should fail.
	_, err = mp.Chat(context.Background(), &provider.ChatRequest{})
	if err == nil {
		t.Fatal("expected error when responses exhausted, got nil")
	}
	if got := err.Error(); got != "mock provider: no more scripted responses (call #2)" {
		t.Errorf("error = %q, want %q", got, "mock provider: no more scripted responses (call #2)")
	}
}

func TestMockProvider_ErrorResponse(t *testing.T) {
	expectedErr := errors.New("rate limit exceeded")
	mp := New(ErrorResponse(expectedErr))

	_, err := mp.Chat(context.Background(), &provider.ChatRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestMockProvider_CallRecording(t *testing.T) {
	mp := New(TextResponse("a"), TextResponse("b"))

	req1 := &provider.ChatRequest{
		Model:     "claude-sonnet",
		MaxTokens: 1024,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}},
		},
	}
	req2 := &provider.ChatRequest{
		Model:     "claude-opus",
		MaxTokens: 4096,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}}},
		},
	}

	_, _ = mp.Chat(context.Background(), req1)
	_, _ = mp.Chat(context.Background(), req2)

	calls := mp.Calls()
	if len(calls) != 2 {
		t.Fatalf("CallCount = %d, want 2", len(calls))
	}

	if calls[0].Model != "claude-sonnet" {
		t.Errorf("calls[0].Model = %q, want %q", calls[0].Model, "claude-sonnet")
	}
	if calls[0].MaxTokens != 1024 {
		t.Errorf("calls[0].MaxTokens = %d, want 1024", calls[0].MaxTokens)
	}
	if calls[1].Model != "claude-opus" {
		t.Errorf("calls[1].Model = %q, want %q", calls[1].Model, "claude-opus")
	}
	if calls[1].MaxTokens != 4096 {
		t.Errorf("calls[1].MaxTokens = %d, want 4096", calls[1].MaxTokens)
	}

	if mp.CallCount() != 2 {
		t.Errorf("CallCount() = %d, want 2", mp.CallCount())
	}
}

func TestStreamBuilder_DefaultUsage(t *testing.T) {
	stream := BuildStream(Response{Text: "test"})

	var usage *types.Usage
	for ev := range stream.Events {
		if ev.Type == types.StreamEventMessageEnd {
			usage = ev.Usage
		}
	}

	if usage == nil {
		t.Fatal("expected usage in message_end event, got nil")
	}
	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", usage.OutputTokens)
	}
}

func TestStreamBuilder_CustomStopReason(t *testing.T) {
	stream := BuildStream(Response{
		Text:       "partial",
		StopReason: "max_tokens",
	})

	var stopReason string
	for ev := range stream.Events {
		if ev.Type == types.StreamEventMessageEnd {
			stopReason = ev.StopReason
		}
	}

	if stopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want %q", stopReason, "max_tokens")
	}
}

func TestStreamBuilder_CustomUsage(t *testing.T) {
	customUsage := &types.Usage{
		InputTokens:  500,
		OutputTokens: 200,
		CacheRead:    300,
	}
	stream := BuildStream(Response{
		Text:  "cached response",
		Usage: customUsage,
	})

	var usage *types.Usage
	for ev := range stream.Events {
		if ev.Type == types.StreamEventMessageEnd {
			usage = ev.Usage
		}
	}

	if usage == nil {
		t.Fatal("expected usage in message_end event, got nil")
	}
	if usage.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", usage.InputTokens)
	}
	if usage.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", usage.OutputTokens)
	}
	if usage.CacheRead != 300 {
		t.Errorf("CacheRead = %d, want 300", usage.CacheRead)
	}
}

func TestMockProvider_CountTokens(t *testing.T) {
	mp := New()

	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "hello world 1234"}, // 16 chars -> 4 tokens
			},
		},
	}

	count, err := mp.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 16 / 4 = 4
	if count != 4 {
		t.Errorf("CountTokens = %d, want 4", count)
	}
}

func TestMockProvider_ToolCallOnlyResponse(t *testing.T) {
	// Test a response with tool calls but no text.
	toolCalls := []types.ToolCall{
		{ID: "tc_1", Name: "bash", Input: `{"command":"pwd"}`},
	}
	mp := New(Response{ToolCalls: toolCalls})

	stream, err := mp.Chat(context.Background(), &provider.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}

	// Expect: tool_use, message_end (no text event).
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != types.StreamEventToolUse {
		t.Errorf("event[0].Type = %q, want %q", events[0].Type, types.StreamEventToolUse)
	}
	if events[1].Type != types.StreamEventMessageEnd {
		t.Errorf("event[1].Type = %q, want %q", events[1].Type, types.StreamEventMessageEnd)
	}
	if events[1].StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want %q", events[1].StopReason, "tool_use")
	}
}
