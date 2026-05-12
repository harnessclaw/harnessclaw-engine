package stats

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// fakeProvider implements provider.Provider for unit tests.
type fakeProvider struct {
	sentReq *provider.ChatRequest
	events  []types.StreamEvent
	err     error
}

func (f *fakeProvider) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	f.sentReq = req
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan types.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}
func (f *fakeProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) { return 0, nil }
func (f *fakeProvider) Name() string                                                   { return "fake" }

func TestStatsProvider_RecordsUsageWhenSessionPresent(t *testing.T) {
	reg := sessionstats.NewRegistry()
	inner := &fakeProvider{events: []types.StreamEvent{{
		Type:  types.StreamEventMessageEnd,
		Usage: &types.Usage{InputTokens: 100, OutputTokens: 50, CacheRead: 20, ThinkingTokens: 8},
	}}}
	sp := New(inner, reg)

	ctx := sessionstats.WithSessionID(context.Background(), "sess_abc")
	stream, err := sp.Chat(ctx, &provider.ChatRequest{
		Model:     "opus",
		Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}}}},
		System:    "you are useful",
		MaxTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream.Events {
	}

	tr := reg.Get("sess_abc")
	if tr == nil {
		t.Fatalf("tracker not created")
	}
	s := tr.Snapshot()
	if s.InputTokens != 100 || s.OutputTokens != 50 || s.ThinkingTokens != 8 {
		t.Errorf("usage not recorded: %+v", s)
	}
	if s.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1", s.LLMCalls)
	}
	if len(s.PerModel) != 1 || s.PerModel[0].Model != "opus" {
		t.Errorf("PerModel: %+v", s.PerModel)
	}
	if s.ContextWindow.Limit != 1024 {
		t.Errorf("ContextWindow.Limit = %d, want 1024", s.ContextWindow.Limit)
	}
	if s.ContextWindow.History == 0 || s.ContextWindow.SystemPrompt == 0 {
		t.Errorf("ContextWindow composition zero: %+v", s.ContextWindow)
	}
}

func TestStatsProvider_NoSessionIDIsPassThrough(t *testing.T) {
	reg := sessionstats.NewRegistry()
	inner := &fakeProvider{events: []types.StreamEvent{{Type: types.StreamEventMessageEnd, Usage: &types.Usage{InputTokens: 1}}}}
	sp := New(inner, reg)

	stream, err := sp.Chat(context.Background(), &provider.ChatRequest{Model: "opus", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream.Events {
	}
	if got := reg.Get("anything"); got != nil {
		t.Errorf("unexpected tracker")
	}
}

func TestStatsProvider_AttributesToSubAgentRow(t *testing.T) {
	reg := sessionstats.NewRegistry()
	inner := &fakeProvider{events: []types.StreamEvent{{Type: types.StreamEventMessageEnd, Usage: &types.Usage{InputTokens: 40, OutputTokens: 10}}}}
	sp := New(inner, reg)

	tr := reg.GetOrCreate("sess_abc")
	tr.StartSubAgent("run_e5", "sub_e5", "researcher", "")

	ctx := sessionstats.WithSessionID(context.Background(), "sess_abc")
	ctx = sessionstats.WithAgentRunID(ctx, "run_e5")
	stream, err := sp.Chat(ctx, &provider.ChatRequest{Model: "sonnet", MaxTokens: 256})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream.Events {
	}

	s := tr.Snapshot()
	if len(s.SubAgents) != 1 || s.SubAgents[0].InputTokens != 40 {
		t.Errorf("sub-agent row not updated: %+v", s.SubAgents)
	}
	if s.SubAgents[0].Model != "sonnet" {
		t.Errorf("model not set on row: %+v", s.SubAgents[0])
	}
}

func TestClassifyContext_BucketsContentBlocks(t *testing.T) {
	req := &provider.ChatRequest{
		Model:     "opus",
		MaxTokens: 8192,
		System:    "you are useful",
		Tools:     []provider.ToolSchema{{Name: "bash", Description: "run shell", InputSchema: map[string]any{"x": "y"}}},
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "first user message"}}},
			{Role: types.RoleAssistant, Content: []types.ContentBlock{{Type: types.ContentTypeToolResult, ToolResult: "the result of the tool call"}}},
		},
	}
	used, limit, hist, tr, sys := classifyContext(req)
	if limit != 8192 {
		t.Errorf("limit = %d, want 8192", limit)
	}
	if hist == 0 || tr == 0 || sys == 0 {
		t.Errorf("composition zero: hist=%d tr=%d sys=%d", hist, tr, sys)
	}
	if used != hist+tr+sys {
		t.Errorf("used (%d) != hist+tr+sys (%d)", used, hist+tr+sys)
	}
}
