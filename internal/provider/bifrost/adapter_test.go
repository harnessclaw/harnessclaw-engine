package bifrost

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// ==============================
// Account interface tests
// ==============================

func TestAccount_GetConfiguredProviders(t *testing.T) {
	acct := &account{provider: schemas.Anthropic}
	providers, err := acct.GetConfiguredProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	if providers[0] != schemas.Anthropic {
		t.Errorf("expected Anthropic, got %s", providers[0])
	}
}

func TestAccount_GetKeysForProvider(t *testing.T) {
	acct := &account{
		provider: schemas.Anthropic,
		apiKey:   "sk-test-key",
		model:    "claude-sonnet-4-20250514",
	}

	keys, err := acct.GetKeysForProvider(context.Background(), schemas.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Weight != 1.0 {
		t.Errorf("expected weight 1.0, got %f", keys[0].Weight)
	}
	if len(keys[0].Models) != 1 || keys[0].Models[0] != "claude-sonnet-4-20250514" {
		t.Errorf("unexpected models: %v", keys[0].Models)
	}
}

func TestAccount_GetKeysForProvider_WrongProvider(t *testing.T) {
	acct := &account{provider: schemas.Anthropic}
	_, err := acct.GetKeysForProvider(context.Background(), schemas.OpenAI)
	if err == nil {
		t.Fatal("expected error for wrong provider")
	}
}

func TestAccount_GetConfigForProvider(t *testing.T) {
	acct := &account{
		provider:       schemas.Anthropic,
		maxConcurrency: 50,
		bufferSize:     200,
	}
	cfg, err := acct.GetConfigForProvider(schemas.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConcurrencyAndBufferSize.Concurrency != 50 {
		t.Errorf("expected concurrency 50, got %d", cfg.ConcurrencyAndBufferSize.Concurrency)
	}
	if cfg.ConcurrencyAndBufferSize.BufferSize != 200 {
		t.Errorf("expected buffer 200, got %d", cfg.ConcurrencyAndBufferSize.BufferSize)
	}
}

func TestAccount_GetConfigForProvider_WithProxy(t *testing.T) {
	acct := &account{
		provider: schemas.Anthropic,
		proxyURL: "http://proxy.local:8080",
	}
	cfg, err := acct.GetConfigForProvider(schemas.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxyConfig == nil {
		t.Fatal("expected proxy config")
	}
	if cfg.ProxyConfig.URL != "http://proxy.local:8080" {
		t.Errorf("unexpected proxy URL: %s", cfg.ProxyConfig.URL)
	}
	if cfg.ProxyConfig.Type != schemas.HTTPProxy {
		t.Errorf("expected HTTPProxy, got %s", cfg.ProxyConfig.Type)
	}
}

func TestAccount_GetConfigForProvider_WithBaseURL(t *testing.T) {
	acct := &account{
		provider: schemas.Anthropic,
		baseURL:  "https://custom-gateway.example.com",
	}
	cfg, err := acct.GetConfigForProvider(schemas.Anthropic)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NetworkConfig.BaseURL != "https://custom-gateway.example.com" {
		t.Errorf("unexpected base URL: %s", cfg.NetworkConfig.BaseURL)
	}
}

func TestAccount_GetConfigForProvider_WrongProvider(t *testing.T) {
	acct := &account{provider: schemas.Anthropic}
	_, err := acct.GetConfigForProvider(schemas.OpenAI)
	if err == nil {
		t.Fatal("expected error for wrong provider")
	}
}

// ==============================
// Constructor validation tests
// ==============================

func TestNew_MissingAPIKey(t *testing.T) {
	_, err := New(Config{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-20250514",
	})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(Config{
		Provider: schemas.Anthropic,
		APIKey:   "sk-test",
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestNew_DefaultProvider(t *testing.T) {
	// Provider defaults to Anthropic.
	cfg := Config{
		APIKey: "sk-test",
		Model:  "test-model",
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	if a.providerKey != schemas.Anthropic {
		t.Errorf("expected Anthropic, got %s", a.providerKey)
	}
	if a.Name() != "bifrost" {
		t.Errorf("expected name 'bifrost', got %q", a.Name())
	}
}

// ==============================
// Message conversion tests
// ==============================

func TestConvertMessages_SystemPrompt(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}}},
	}
	result := convertMessages(msgs, "You are helpful.", true, nil)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(result))
	}
	if result[0].Role != schemas.ChatMessageRoleSystem {
		t.Errorf("first message should be system, got %s", result[0].Role)
	}
	if result[0].Content == nil || result[0].Content.ContentStr == nil || *result[0].Content.ContentStr != "You are helpful." {
		t.Errorf("unexpected system content: %+v", result[0].Content)
	}
	if result[1].Role != schemas.ChatMessageRoleUser {
		t.Errorf("second message should be user, got %s", result[1].Role)
	}
}

func TestConvertMessages_NoSystemPrompt(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}},
	}
	result := convertMessages(msgs, "", true, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestConvertMessages_ToolResult(t *testing.T) {
	// Pair the tool_result with its producing assistant tool_use so the
	// orphan-message sanitiser doesn't drop it. See
	// TestConvertMessages_DropsOrphanToolResponses for the case that
	// requires the sanitiser.
	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{{
				Type:      types.ContentTypeToolUse,
				ToolUseID: "tool-123",
				ToolName:  "compute",
				ToolInput: "{}",
			}},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type:      types.ContentTypeToolResult,
				ToolUseID: "tool-123",
				ToolResult: "42",
			}},
		},
	}
	result := convertMessages(msgs, "", true, nil)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (assistant + tool), got %d", len(result))
	}
	if result[1].Role != schemas.ChatMessageRoleTool {
		t.Errorf("expected tool role on second msg, got %s", result[1].Role)
	}
	if result[1].ChatToolMessage == nil || result[1].ChatToolMessage.ToolCallID == nil {
		t.Fatal("expected ChatToolMessage with ToolCallID")
	}
	if *result[1].ChatToolMessage.ToolCallID != "tool-123" {
		t.Errorf("unexpected tool call ID: %s", *result[1].ChatToolMessage.ToolCallID)
	}
}

func TestConvertMessages_ToolUse(t *testing.T) {
	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "Let me call the tool."},
				{Type: types.ContentTypeToolUse, ToolUseID: "call-1", ToolName: "echo", ToolInput: `{"text":"ping"}`},
			},
		},
	}
	result := convertMessages(msgs, "", true, nil)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	msg := result[0]
	if msg.Role != schemas.ChatMessageRoleAssistant {
		t.Errorf("expected assistant role, got %s", msg.Role)
	}
	if msg.ChatAssistantMessage == nil || len(msg.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatal("expected 1 tool call")
	}
	tc := msg.ChatAssistantMessage.ToolCalls[0]
	if tc.ID == nil || *tc.ID != "call-1" {
		t.Errorf("unexpected tool call ID: %v", tc.ID)
	}
	if tc.Function.Name == nil || *tc.Function.Name != "echo" {
		t.Errorf("unexpected tool name: %v", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"text":"ping"}` {
		t.Errorf("unexpected arguments: %s", tc.Function.Arguments)
	}
}

func TestConvertMessages_SkipSystemRole(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "system text"}}},
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}},
	}
	result := convertMessages(msgs, "", true, nil)
	// System role messages in the input are skipped (system prompt is separate param).
	if len(result) != 1 {
		t.Fatalf("expected 1 message (system role skipped), got %d", len(result))
	}
}

// ==============================
// Tool conversion tests
// ==============================

func TestConvertTools(t *testing.T) {
	tools := []provider.ToolSchema{
		{
			Name:        "echo",
			Description: "Echoes text",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "text to echo"},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "add",
			Description: "Adds two numbers",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number"},
					"b": map[string]any{"type": "number"},
				},
				"required": []string{"a", "b"},
			},
		},
	}

	result := ConvertTools(tools)
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}

	if result[0].Type != schemas.ChatToolTypeFunction {
		t.Errorf("expected function type, got %s", result[0].Type)
	}
	if result[0].Function == nil || result[0].Function.Name != "echo" {
		t.Errorf("unexpected tool name: %+v", result[0].Function)
	}
	if result[0].Function.Description == nil || *result[0].Function.Description != "Echoes text" {
		t.Errorf("unexpected description: %+v", result[0].Function.Description)
	}
	if result[0].Function.Parameters == nil {
		t.Fatal("expected parameters")
	}
	if result[0].Function.Parameters.Type != "object" {
		t.Errorf("unexpected type: %s", result[0].Function.Parameters.Type)
	}
	if len(result[0].Function.Parameters.Required) != 1 || result[0].Function.Parameters.Required[0] != "text" {
		t.Errorf("unexpected required: %v", result[0].Function.Parameters.Required)
	}
}

func TestConvertTools_Empty(t *testing.T) {
	result := ConvertTools(nil)
	if result != nil {
		t.Errorf("expected nil for empty tools, got %v", result)
	}
}

// ==============================
// Stream event mapping tests
// ==============================

func TestConsumeStream_TextDelta(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 3)
	out := make(chan types.StreamEvent, 10)

	content := "Hello!"
	finishReason := "stop"

	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content: &content,
					},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
			}},
		},
	}
	close(stream)

	a := &Adapter{}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != types.StreamEventText || events[0].Text != "Hello!" {
		t.Errorf("unexpected text event: %+v", events[0])
	}
	if events[1].Type != types.StreamEventMessageEnd {
		t.Errorf("expected message_end, got %s", events[1].Type)
	}
	if events[1].StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", events[1].StopReason)
	}
	if events[1].Usage == nil || events[1].Usage.InputTokens != 10 || events[1].Usage.OutputTokens != 5 {
		t.Errorf("unexpected usage: %+v", events[1].Usage)
	}
}

func TestConsumeStream_ToolCallDelta(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 4)
	out := make(chan types.StreamEvent, 10)

	toolID := "tool-1"
	toolName := "echo"
	finishReason := "tool_calls"

	// First chunk: tool call start with partial args.
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							Index: 0,
							ID:    &toolID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &toolName,
								Arguments: `{"text":`,
							},
						}},
					},
				},
			}},
		},
	}

	// Second chunk: more args.
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							Index: 0,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Arguments: `"ping"}`,
							},
						}},
					},
				},
			}},
		},
	}

	// Final chunk with finish reason.
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     15,
				CompletionTokens: 8,
				TotalTokens:      23,
			},
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
			}},
		},
	}
	close(stream)

	a := &Adapter{}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	// Should have tool_use + message_end.
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != types.StreamEventToolUse {
		t.Errorf("expected tool_use, got %s", events[0].Type)
	}
	if events[0].ToolCall == nil {
		t.Fatal("expected ToolCall")
	}
	if events[0].ToolCall.ID != "tool-1" {
		t.Errorf("unexpected tool ID: %s", events[0].ToolCall.ID)
	}
	if events[0].ToolCall.Name != "echo" {
		t.Errorf("unexpected tool name: %s", events[0].ToolCall.Name)
	}
	if events[0].ToolCall.Input != `{"text":"ping"}` {
		t.Errorf("unexpected tool input: %s", events[0].ToolCall.Input)
	}
	if events[1].Type != types.StreamEventMessageEnd {
		t.Errorf("expected message_end, got %s", events[1].Type)
	}
	if events[1].StopReason != "tool_use" {
		t.Errorf("expected 'tool_use', got %q", events[1].StopReason)
	}
}

func TestConsumeStream_ErrorChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	out := make(chan types.StreamEvent, 10)

	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: "rate limit exceeded",
			},
		},
	}
	close(stream)

	a := &Adapter{}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	if events[0].Type != types.StreamEventError {
		t.Errorf("expected error event, got %s", events[0].Type)
	}
	if events[0].Error == nil {
		t.Fatal("expected error")
	}
}

func TestConsumeStream_NilChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	out := make(chan types.StreamEvent, 10)

	stream <- nil // Should be skipped.
	finishReason := "stop"
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
			}},
		},
	}
	close(stream)

	a := &Adapter{}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event (nil skipped), got %d", len(events))
	}
}

func TestConsumeStream_CacheTokens(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	out := make(chan types.StreamEvent, 10)

	finishReason := "stop"
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens:  80,
					CachedWriteTokens: 20,
				},
			},
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
			}},
		},
	}
	close(stream)

	a := &Adapter{}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	u := events[0].Usage
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.CacheRead != 80 {
		t.Errorf("expected CacheRead=80, got %d", u.CacheRead)
	}
	if u.CacheWrite != 20 {
		t.Errorf("expected CacheWrite=20, got %d", u.CacheWrite)
	}
}

// ==============================
// Role & reason mapping tests
// ==============================

func TestMapRole(t *testing.T) {
	tests := []struct {
		in  types.Role
		out schemas.ChatMessageRole
	}{
		{types.RoleUser, schemas.ChatMessageRoleUser},
		{types.RoleAssistant, schemas.ChatMessageRoleAssistant},
		{types.RoleSystem, schemas.ChatMessageRoleSystem},
		{"unknown", schemas.ChatMessageRoleUser},
	}
	for _, tt := range tests {
		got := mapRole(tt.in)
		if got != tt.out {
			t.Errorf("mapRole(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"custom_reason", "custom_reason"},
	}
	for _, tt := range tests {
		got := mapFinishReason(tt.in)
		if got != tt.out {
			t.Errorf("mapFinishReason(%q) = %q, want %q", tt.in, got, tt.out)
		}
	}
}

// ==============================
// Tool call accumulation tests
// ==============================

func TestAccumulateToolCall(t *testing.T) {
	name := "echo"
	id := "call-1"

	var acc []toolCallAccumulator

	// First delta: new tool call.
	acc = accumulateToolCall(acc, schemas.ChatAssistantMessageToolCall{
		Index: 0,
		ID:    &id,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &name,
			Arguments: `{"text":`,
		},
	})
	if len(acc) != 1 {
		t.Fatalf("expected 1 accumulator, got %d", len(acc))
	}
	if acc[0].id != "call-1" || acc[0].name != "echo" {
		t.Errorf("unexpected: id=%q name=%q", acc[0].id, acc[0].name)
	}

	// Second delta: append to same tool call.
	acc = accumulateToolCall(acc, schemas.ChatAssistantMessageToolCall{
		Index: 0,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Arguments: `"ping"}`,
		},
	})
	if len(acc) != 1 {
		t.Fatalf("expected 1 accumulator after append, got %d", len(acc))
	}
	if acc[0].args.String() != `{"text":"ping"}` {
		t.Errorf("unexpected args: %q", acc[0].args.String())
	}
}

func TestAccumulateToolCall_MultipleTools(t *testing.T) {
	name1, id1 := "echo", "call-1"
	name2, id2 := "add", "call-2"

	var acc []toolCallAccumulator

	acc = accumulateToolCall(acc, schemas.ChatAssistantMessageToolCall{
		Index: 0, ID: &id1,
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name1, Arguments: `{"a":1}`},
	})
	acc = accumulateToolCall(acc, schemas.ChatAssistantMessageToolCall{
		Index: 1, ID: &id2,
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name2, Arguments: `{"x":2}`},
	})

	if len(acc) != 2 {
		t.Fatalf("expected 2 accumulators, got %d", len(acc))
	}
	if acc[0].name != "echo" || acc[1].name != "add" {
		t.Errorf("unexpected names: %q, %q", acc[0].name, acc[1].name)
	}
}

// ==============================
// SetModel / Fallback state tests
// ==============================

func TestSetModel_And_CurrentModel(t *testing.T) {
	a := &Adapter{defaultModel: "default-model"}

	if a.currentModel() != "default-model" {
		t.Errorf("expected default model, got %q", a.currentModel())
	}

	a.SetModel("custom-model")
	if a.currentModel() != "custom-model" {
		t.Errorf("expected custom model, got %q", a.currentModel())
	}

	a.SetModel("")
	if a.currentModel() != "default-model" {
		t.Errorf("expected default model after reset, got %q", a.currentModel())
	}
}

func TestFallbackState(t *testing.T) {
	a := &Adapter{
		defaultModel:  "primary",
		fallbackModel: "fallback",
	}

	if a.IsUsingFallback() {
		t.Error("should not be using fallback initially")
	}

	a.mu.Lock()
	a.usingFallback = true
	a.mu.Unlock()

	if !a.IsUsingFallback() {
		t.Error("should be using fallback")
	}
	if a.currentModel() != "fallback" {
		t.Errorf("expected fallback model, got %q", a.currentModel())
	}

	a.ResetFallback()
	if a.IsUsingFallback() {
		t.Error("should not be using fallback after reset")
	}
	if a.currentModel() != "primary" {
		t.Errorf("expected primary model after reset, got %q", a.currentModel())
	}
}

// ==============================
// CountTokens tests
// ==============================

func TestCountTokens(t *testing.T) {
	a := &Adapter{}
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Hello, this is a test message."}}},
	}
	tokens, err := a.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	// "Hello, this is a test message." = 30 chars / 4 = 7
	if tokens != 7 {
		t.Errorf("expected ~7 tokens, got %d", tokens)
	}
}

// ==============================
// buildParams tests
// ==============================

func TestBuildParams_Empty(t *testing.T) {
	req := &provider.ChatRequest{}
	a := &Adapter{}
	params := a.buildParams(req)
	if params != nil {
		t.Error("expected nil params for empty request")
	}
}

func TestBuildParams_WithToolsAndTemp(t *testing.T) {
	req := &provider.ChatRequest{
		Temperature: 0.7,
		MaxTokens:   2048,
		Tools: []provider.ToolSchema{
			{Name: "test", Description: "test tool", InputSchema: map[string]any{"type": "object"}},
		},
	}
	a := &Adapter{}
	params := a.buildParams(req)
	if params == nil {
		t.Fatal("expected non-nil params")
	}
	if params.Temperature == nil || *params.Temperature != 0.7 {
		t.Errorf("unexpected temperature: %v", params.Temperature)
	}
	if params.MaxCompletionTokens == nil || *params.MaxCompletionTokens != 2048 {
		t.Errorf("unexpected max tokens: %v", params.MaxCompletionTokens)
	}
	if len(params.Tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(params.Tools))
	}
}

// TestBuildParams_AgentDefaultsApplied confirms agent-level defaults
// (pre-scaled / pre-capped by the builder) kick in only when the
// per-call request leaves Temperature/MaxTokens at zero.
func TestBuildParams_AgentDefaultsApplied(t *testing.T) {
	a := &Adapter{
		defaultTemperature: 0.5,
		defaultMaxTokens:   1234,
	}
	params := a.buildParams(&provider.ChatRequest{})
	if params == nil {
		t.Fatal("expected params populated from defaults")
	}
	if params.Temperature == nil || *params.Temperature != 0.5 {
		t.Errorf("default temp not applied: %v", params.Temperature)
	}
	if params.MaxCompletionTokens == nil || *params.MaxCompletionTokens != 1234 {
		t.Errorf("default max_tokens not applied: %v", params.MaxCompletionTokens)
	}

	// Request fields beat defaults when set.
	params = a.buildParams(&provider.ChatRequest{Temperature: 0.9, MaxTokens: 4096})
	if *params.Temperature != 0.9 || *params.MaxCompletionTokens != 4096 {
		t.Errorf("request values should win over defaults: %+v", params)
	}
}

// ==============================
// jsonBuilder tests
// ==============================

func TestJsonBuilder(t *testing.T) {
	var b jsonBuilder
	if b.String() != "{}" {
		t.Errorf("empty builder should return '{}', got %q", b.String())
	}

	b.Write(`{"a":`)
	b.Write(`1}`)
	if b.String() != `{"a":1}` {
		t.Errorf("unexpected result: %q", b.String())
	}
}

func TestConsumeStream_ExtractsReasoningTokensFromCompletionDetails(t *testing.T) {
	a := &Adapter{}
	in := make(chan *schemas.BifrostStreamChunk, 1)
	out := make(chan types.StreamEvent, 4)

	finishReason := "stop"
	in <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{},
				},
			}},
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens:  20,
					CachedWriteTokens: 5,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 17,
				},
			},
		},
	}
	close(in)

	go func() { _ = a.consumeStream(in, out); close(out) }()

	var got *types.Usage
	for ev := range out {
		if ev.Type == types.StreamEventMessageEnd && ev.Usage != nil {
			got = ev.Usage
		}
	}
	if got == nil {
		t.Fatalf("expected MessageEnd with Usage, got none")
	}
	if got.ThinkingTokens != 17 {
		t.Errorf("ThinkingTokens = %d, want 17", got.ThinkingTokens)
	}
	if got.CacheRead != 20 || got.CacheWrite != 5 {
		t.Errorf("cache fields wrong: read=%d write=%d", got.CacheRead, got.CacheWrite)
	}
}

// TestConsumeStream_UsageOnSyntheticFinalChunk simulates how bifrost's
// OpenAI provider actually forwards chunks for OpenAI-compatible gateways
// like 讯飞 MaaS: a chunk with finish_reason but zero usage (bifrost
// strips it), followed by a usage-bearing chunk with no choices.
// The adapter must defer MessageEnd until the usage-only chunk lands.
func TestConsumeStream_UsageOnSyntheticFinalChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 4)
	out := make(chan types.StreamEvent, 10)

	content := "hello"
	finishReason := "stop"

	// chunk 1: text delta
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Model: "xopglm51",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content: &content,
					},
				},
			}},
		},
	}
	// chunk 2: finish_reason, no usage (bifrost cleared it)
	emptyContent := ""
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Model: "xopglm51",
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &emptyContent},
				},
			}},
		},
	}
	// chunk 3 (bifrost synthetic): usage, no choices, no finish
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Model: "xopglm51",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     7,
				CompletionTokens: 13,
				TotalTokens:      20,
			},
		},
	}
	close(stream)

	a := &Adapter{logger: zap.NewNop()}
	go func() {
		a.consumeStream(stream, out)
		close(out)
	}()

	var events []types.StreamEvent
	for evt := range out {
		events = append(events, evt)
	}

	var msgEnd *types.StreamEvent
	for i := range events {
		if events[i].Type == types.StreamEventMessageEnd {
			msgEnd = &events[i]
		}
	}
	if msgEnd == nil {
		t.Fatalf("expected MessageEnd event, got events: %+v", events)
	}
	if msgEnd.Usage == nil {
		t.Fatalf("MessageEnd.Usage is nil; want {7,13}")
	}
	if msgEnd.Usage.InputTokens != 7 || msgEnd.Usage.OutputTokens != 13 {
		t.Errorf("usage = %+v, want {InputTokens:7 OutputTokens:13}", msgEnd.Usage)
	}
	if msgEnd.Model != "xopglm51" {
		t.Errorf("Model = %q, want xopglm51", msgEnd.Model)
	}
	if msgEnd.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn (mapped from 'stop')", msgEnd.StopReason)
	}
}

// TestConvertSingleMessage_AssistantReasoningRoundTrip ensures that the
// thinking-mode reasoning_content captured from an upstream stream is
// faithfully reattached to the wire when the message is replayed on the
// next request. DeepSeek thinking models REJECT requests that omit it
// ("The reasoning_content in the thinking mode must be passed back").
func TestConvertSingleMessage_AssistantReasoningRoundTrip(t *testing.T) {
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "答案是 42。"},
		},
		ReasoningContent: "用户问 X,我推导 X = 42,所以...",
	}
	bf := convertSingleMessage(msg, true)
	if bf == nil || bf.ChatAssistantMessage == nil {
		t.Fatalf("expected ChatAssistantMessage to be set, got %+v", bf)
	}
	if bf.ChatAssistantMessage.Reasoning == nil {
		t.Fatalf("expected Reasoning to be set on assistant wire message")
	}
	if *bf.ChatAssistantMessage.Reasoning != msg.ReasoningContent {
		t.Errorf("reasoning round-trip mismatch: got %q, want %q",
			*bf.ChatAssistantMessage.Reasoning, msg.ReasoningContent)
	}
}

// TestConsumeStream_CapturesDeltaReasoning verifies that delta.Reasoning
// chunks (DeepSeek's reasoning_content, mapped by bifrost SDK) are
// accumulated and surfaced on MessageEnd.Reasoning.
func TestConsumeStream_CapturesDeltaReasoning(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 4)
	out := make(chan types.StreamEvent, 10)

	r1, r2 := "思考 step 1。", "思考 step 2。"
	content := "答案"
	finish := "stop"

	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Reasoning: &r1},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Reasoning: &r2},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: &finish,
			}},
		},
	}
	close(stream)

	a := &Adapter{logger: zap.NewNop()}
	go func() { a.consumeStream(stream, out); close(out) }()

	var msgEnd *types.StreamEvent
	for ev := range out {
		if ev.Type == types.StreamEventMessageEnd {
			msgEnd = &ev
		}
	}
	if msgEnd == nil {
		t.Fatal("expected MessageEnd")
	}
	want := r1 + r2
	if msgEnd.Reasoning != want {
		t.Errorf("Reasoning = %q, want %q", msgEnd.Reasoning, want)
	}
}

// TestBuildChatRequest_ThinkingControlled verifies the thinking toggle
// reaches the wire via ExtraParams as DeepSeek's nested
// {"thinking": {"type": "enabled"|"disabled"}} form, and stays absent
// when nil. See https://api-docs.deepseek.com/zh-cn/guides/thinking_mode .
func TestBuildChatRequest_ThinkingControlled(t *testing.T) {
	cases := []struct {
		name     string
		flag     *bool
		wantType string // empty → field absent
	}{
		{"nil leaves ExtraParams untouched", nil, ""},
		{"false sends type=disabled", boolPtr(false), "disabled"},
		{"true sends type=enabled", boolPtr(true), "enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{
				providerKey:    schemas.OpenAI,
				defaultModel:   "deepseek-chat",
				enableThinking: tc.flag,
				quirks:         &ProviderQuirks{ThinkingParamStyle: "deepseek_type"},
			}
			bfReq := a.buildChatRequest("deepseek-chat", &provider.ChatRequest{
				MaxTokens: 128,
				Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}}},
			})
			if tc.wantType == "" {
				if bfReq.Params != nil && bfReq.Params.ExtraParams != nil {
					if _, present := bfReq.Params.ExtraParams["thinking"]; present {
						t.Errorf("expected no thinking in ExtraParams, got %+v", bfReq.Params.ExtraParams)
					}
				}
				return
			}
			if bfReq.Params == nil || bfReq.Params.ExtraParams == nil {
				t.Fatalf("expected Params.ExtraParams set, got %+v", bfReq.Params)
			}
			raw, ok := bfReq.Params.ExtraParams["thinking"]
			if !ok {
				t.Fatalf("thinking missing from ExtraParams: %+v", bfReq.Params.ExtraParams)
			}
			obj, ok := raw.(map[string]string)
			if !ok {
				t.Fatalf("thinking should be map[string]string, got %T: %+v", raw, raw)
			}
			if obj["type"] != tc.wantType {
				t.Errorf("thinking.type = %q, want %q", obj["type"], tc.wantType)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestConsumeStream_LogsRawUsageJSON verifies that the verbose debug
// log captures the full BifrostLLMUsage object as JSON, including
// provider-specific subfields, so operators can troubleshoot
// missing/unexpected tokens against the upstream wire format.
func TestConsumeStream_LogsRawUsageJSON(t *testing.T) {
	core, recorded := observer.New(zap.DebugLevel)
	a := &Adapter{logger: zap.New(core)}

	stream := make(chan *schemas.BifrostStreamChunk, 1)
	out := make(chan types.StreamEvent, 4)
	finish := "stop"
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Model: "deepseek-v4-flash",
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     1234,
				CompletionTokens: 56,
				TotalTokens:      1290,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens: 800,
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					ReasoningTokens: 9,
				},
			},
			Choices: []schemas.BifrostResponseChoice{{FinishReason: &finish}},
		},
	}
	close(stream)

	go func() { a.consumeStream(stream, out); close(out) }()
	for range out {
	}

	var rawJSON string
	for _, e := range recorded.All() {
		if e.Message == "bifrost raw usage" {
			for _, f := range e.Context {
				if f.Key == "usage_json" {
					if b, ok := f.Interface.([]byte); ok {
						rawJSON = string(b)
					}
				}
			}
		}
	}
	if rawJSON == "" {
		t.Fatalf("expected 'bifrost raw usage' log entry with usage_json field; got %d entries", len(recorded.All()))
	}
	// Spot-check that key provider subfields made it into the JSON dump.
	for _, want := range []string{"\"prompt_tokens\":1234", "\"cached_read_tokens\":800", "\"reasoning_tokens\":9"} {
		if !strings.Contains(rawJSON, want) {
			t.Errorf("usage_json missing %q;\nfull: %s", want, rawJSON)
		}
	}
}

// TestConvertMessages_SkipsReasoningWhenDisabled verifies that prior
// assistant ReasoningContent is dropped on the wire when the operator
// disables thinking. Keeping it would inflate input tokens every turn
// without serving any provider that requires it (verified DeepSeek
// doesn't, on 2026-05-13).
func TestConvertMessages_SkipsReasoningWhenDisabled(t *testing.T) {
	msgs := []types.Message{
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeText, Text: "答案是 42"},
			},
			ReasoningContent: "用户问 X, 我推导 X = 42, 所以...",
		},
	}

	// Include = true → reasoning forwarded.
	withReasoning := convertMessages(msgs, "", true, nil)
	if len(withReasoning) != 1 || withReasoning[0].ChatAssistantMessage == nil ||
		withReasoning[0].ChatAssistantMessage.Reasoning == nil ||
		*withReasoning[0].ChatAssistantMessage.Reasoning != msgs[0].ReasoningContent {
		t.Errorf("with includeReasoning=true: expected reasoning forwarded, got %+v", withReasoning[0].ChatAssistantMessage)
	}

	// Include = false → reasoning suppressed (no ChatAssistantMessage at all,
	// because there are no tool_calls either).
	withoutReasoning := convertMessages(msgs, "", false, nil)
	if len(withoutReasoning) != 1 {
		t.Fatalf("expected 1 message, got %d", len(withoutReasoning))
	}
	if withoutReasoning[0].ChatAssistantMessage != nil &&
		withoutReasoning[0].ChatAssistantMessage.Reasoning != nil {
		t.Errorf("with includeReasoning=false: reasoning leaked through: %+v", withoutReasoning[0].ChatAssistantMessage.Reasoning)
	}
}

// TestBuildChatRequest_DisablingThinkingSuppressesReasoningOnWire ties
// the operator-facing enable_thinking flag to the wire-level behaviour:
// flag=false silences the local reasoning replay even if the model
// keeps emitting reasoning_content (deepseek-v4-flash does this — its
// reasoning is baked into the model so we can't make it stop generating,
// but we can avoid resending it every turn).
func TestBuildChatRequest_DisablingThinkingSuppressesReasoningOnWire(t *testing.T) {
	disabled := false
	a := &Adapter{
		providerKey:    schemas.OpenAI,
		defaultModel:   "deepseek-v4-flash",
		enableThinking: &disabled,
	}
	bfReq := a.buildChatRequest("deepseek-v4-flash", &provider.ChatRequest{
		MaxTokens: 128,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q"}}},
			{
				Role:             types.RoleAssistant,
				Content:          []types.ContentBlock{{Type: types.ContentTypeText, Text: "a"}},
				ReasoningContent: "previous thinking we don't want to resend",
			},
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "q2"}}},
		},
	})

	for i, m := range bfReq.Input {
		if m.ChatAssistantMessage != nil && m.ChatAssistantMessage.Reasoning != nil {
			t.Errorf("message[%d]: reasoning leaked through despite enable_thinking=false: %q",
				i, *m.ChatAssistantMessage.Reasoning)
		}
	}
}

// TestConvertSingleMessage_ToolCallAlwaysHasReasoningField verifies the
// strict schema requirement DeepSeek enforces on thinking-mode tool_calls:
// the reasoning_content / reasoning field MUST be present (any value OK,
// including empty string) — omitting it returns 400.
// Verified against api.deepseek.com / deepseek-v4-flash 2026-05-13.
func TestConvertSingleMessage_ToolCallAlwaysHasReasoningField(t *testing.T) {
	// Even with includeReasoning=false (i.e. enable_thinking disabled),
	// an assistant tool_call must carry the field as an empty string.
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ToolUseID: "c1", ToolName: "noop", ToolInput: "{}"},
		},
		// No ReasoningContent: thinking was disabled so adapter dropped it.
	}
	bf := convertSingleMessage(msg, false)
	if bf == nil || bf.ChatAssistantMessage == nil {
		t.Fatalf("expected assistant message with tool_calls, got %+v", bf)
	}
	if bf.ChatAssistantMessage.Reasoning == nil {
		t.Fatal("tool_call message must include reasoning field (empty allowed); was nil")
	}
	if *bf.ChatAssistantMessage.Reasoning != "" {
		t.Errorf("reasoning should be empty placeholder, got %q", *bf.ChatAssistantMessage.Reasoning)
	}
	if len(bf.ChatAssistantMessage.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(bf.ChatAssistantMessage.ToolCalls))
	}
}

// TestConsumeStream_DropsReasoningWhenThinkingDisabled verifies the
// receive-side companion: when the adapter's enable_thinking=false, the
// per-chunk reasoning text is dropped on the floor, so the resulting
// MessageEnd.Reasoning is empty and the assistant Message saved by the
// engine carries no reasoning_content to inflate the next request.
func TestConsumeStream_DropsReasoningWhenThinkingDisabled(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 4)
	out := make(chan types.StreamEvent, 10)
	r1 := "discardable thinking step"
	content := "answer"
	finish := "stop"

	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Reasoning: &r1},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
				},
			}},
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Usage:   &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
			Choices: []schemas.BifrostResponseChoice{{FinishReason: &finish}},
		},
	}
	close(stream)

	disabled := false
	a := &Adapter{logger: zap.NewNop(), enableThinking: &disabled}
	go func() { a.consumeStream(stream, out); close(out) }()

	var end *types.StreamEvent
	for ev := range out {
		if ev.Type == types.StreamEventMessageEnd {
			end = &ev
		}
	}
	if end == nil {
		t.Fatal("expected MessageEnd event")
	}
	if end.Reasoning != "" {
		t.Errorf("MessageEnd.Reasoning = %q, want empty (thinking disabled)", end.Reasoning)
	}
}

// TestBuildChatRequest_QuirkRoutesThinkingParam verifies that
// ProviderQuirks.ThinkingParamStyle drives which wire format the
// adapter uses to enable/disable reasoning.
func TestBuildChatRequest_QuirkRoutesThinkingParam(t *testing.T) {
	cases := []struct {
		name        string
		style       string
		enabled     bool
		wantPath    string
		wantStringV string
		wantIntV    int
	}{
		{"deepseek disabled", "deepseek_type", false, "extra_params.thinking.type", "disabled", 0},
		{"deepseek enabled", "deepseek_type", true, "extra_params.thinking.type", "enabled", 0},
		{"openai effort enabled", "openai_effort", true, "reasoning.effort", "medium", 0},
		{"openai effort disabled", "openai_effort", false, "reasoning.effort", "none", 0},
		{"anthropic budget enabled", "anthropic_budget", true, "reasoning.max_tokens", "", 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flag := tc.enabled
			a := &Adapter{
				providerKey:    schemas.OpenAI,
				defaultModel:   "x",
				enableThinking: &flag,
				quirks:         &ProviderQuirks{ThinkingParamStyle: tc.style},
			}
			bfReq := a.buildChatRequest("x", &provider.ChatRequest{
				MaxTokens: 16,
				Messages:  []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "h"}}}},
			})
			switch tc.wantPath {
			case "extra_params.thinking.type":
				m, _ := bfReq.Params.ExtraParams["thinking"].(map[string]string)
				if m["type"] != tc.wantStringV {
					t.Errorf("extra_params.thinking.type = %q, want %q", m["type"], tc.wantStringV)
				}
			case "reasoning.effort":
				if bfReq.Params.Reasoning == nil || bfReq.Params.Reasoning.Effort == nil ||
					*bfReq.Params.Reasoning.Effort != tc.wantStringV {
					t.Errorf("reasoning.effort wrong: %+v", bfReq.Params.Reasoning)
				}
			case "reasoning.max_tokens":
				if bfReq.Params.Reasoning == nil || bfReq.Params.Reasoning.MaxTokens == nil ||
					*bfReq.Params.Reasoning.MaxTokens != tc.wantIntV {
					t.Errorf("reasoning.max_tokens wrong: %+v", bfReq.Params.Reasoning)
				}
			}
		})
	}
}

// TestChat_PassthroughExtraParamsOnlyWhenQuirkSet is a structural sanity
// check for the ExtraParams passthrough gate on the Chat() ctx value.
func TestChat_PassthroughExtraParamsOnlyWhenQuirkSet(t *testing.T) {
	cases := []struct {
		name string
		q    *ProviderQuirks
		want bool
	}{
		{"nil quirks", nil, false},
		{"quirk false", &ProviderQuirks{ExtraParamsPassthroughRequired: false}, false},
		{"quirk true", &ProviderQuirks{ExtraParamsPassthroughRequired: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{quirks: tc.q}
			got := a.quirks != nil && a.quirks.ExtraParamsPassthroughRequired
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConvertMessages_DropsOrphanToolResponses is the regression guard
// against `Messages with role 'tool' must be a response to a preceding
// message with 'tool_calls'`. Reproduces the failure mode where a
// tool_result reaches the wire without a matching tool_call:
//   - tool_result_A appears with no preceding assistant tool_calls[A]
//     (e.g. ContractEnforcer inject for a synthetic submit_task_result
//     after the model never actually invoked it; or compactor trimmed
//     the assistant message away).
// The drop preserves valid tool_results that DO have a producing call.
func TestConvertMessages_DropsOrphanToolResponses(t *testing.T) {
	msgs := []types.Message{
		// 1. user turn
		{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "go"}}},
		// 2. assistant only emits tool_use B (no A)
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "calling B"},
			{Type: types.ContentTypeToolUse, ToolUseID: "B", ToolName: "read", ToolInput: "{}"},
		}},
		// 3. orphan tool_result for A (must be dropped)
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "A", ToolName: "submit_task_result", ToolResult: "stale"},
		}},
		// 4. valid tool_result for B (kept)
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "B", ToolName: "read", ToolResult: "file body"},
		}},
	}

	result := convertMessages(msgs, "", false, nil)

	// Expect: user, assistant(toolcalls B), tool(B). Orphan tool(A) gone.
	if len(result) != 3 {
		t.Fatalf("expected 3 messages after orphan drop, got %d: %+v", len(result), result)
	}
	if result[0].Role != schemas.ChatMessageRoleUser {
		t.Errorf("msg[0] role = %s, want user", result[0].Role)
	}
	if result[1].Role != schemas.ChatMessageRoleAssistant {
		t.Errorf("msg[1] role = %s, want assistant", result[1].Role)
	}
	if result[2].Role != schemas.ChatMessageRoleTool {
		t.Errorf("msg[2] role = %s, want tool (kept B)", result[2].Role)
	}
	if result[2].ChatToolMessage == nil ||
		result[2].ChatToolMessage.ToolCallID == nil ||
		*result[2].ChatToolMessage.ToolCallID != "B" {
		t.Errorf("msg[2] tool_call_id mismatch: %+v", result[2].ChatToolMessage)
	}
}

// TestSanitizeToolSequence_StripsMidStreamUnansweredToolCalls reproduces
// the failure mode that survives orphan-only dropping:
//
//   user -> assistant(text + tool_calls A) -> assistant(text + tool_calls B) -> tool(B)
//
// Pass 2 only drops orphan tool messages; it leaves the first assistant
// with an unanswered tool_call A in the middle of the slice, and
// DeepSeek/OpenAI then rejects the entire request with
// `Messages with role 'tool' must be a response to a preceding message
// with 'tool_calls'`. The fix: strip A from that middle assistant.
//
// The LAST assistant (B) is exempt because that's the live state where
// loop.Run has just received the tool_calls and is about to execute
// the tools — its tool_calls are legitimately pending, not orphan.
func TestSanitizeToolSequence_StripsMidStreamUnansweredToolCalls(t *testing.T) {
	core, recorded := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "go"},
		}},
		// Mid-stream assistant: tool_call A never gets a response.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "I'll start with A"},
			{Type: types.ContentTypeToolUse, ToolUseID: "A", ToolName: "read", ToolInput: "{}"},
		}},
		// Last assistant: tool_call B is the pending one that the loop
		// will satisfy in the next dispatch round.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "now B"},
			{Type: types.ContentTypeToolUse, ToolUseID: "B", ToolName: "read", ToolInput: "{}"},
		}},
	}

	result := convertMessages(msgs, "", false, logger)

	// user + assistant(text only, A stripped) + assistant(text + B kept)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// Middle assistant: tool_call A stripped; text retained.
	mid := result[1]
	if mid.Role != schemas.ChatMessageRoleAssistant {
		t.Errorf("msg[1] role = %s, want assistant", mid.Role)
	}
	if mid.ChatAssistantMessage != nil && len(mid.ChatAssistantMessage.ToolCalls) > 0 {
		t.Errorf("msg[1] should have NO tool_calls (A was unanswered), got %d",
			len(mid.ChatAssistantMessage.ToolCalls))
	}
	// Tail assistant: B kept (pending dispatch).
	tail := result[2]
	if tail.ChatAssistantMessage == nil || len(tail.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("msg[2] should keep pending tool_call B, got %+v", tail.ChatAssistantMessage)
	}
	if tail.ChatAssistantMessage.ToolCalls[0].ID == nil ||
		*tail.ChatAssistantMessage.ToolCalls[0].ID != "B" {
		t.Errorf("msg[2] tool_call id = %v, want B", tail.ChatAssistantMessage.ToolCalls[0].ID)
	}

	// Sanity: the sanitizer logged the strip.
	if recorded.FilterMessageSnippet("sanitized tool message sequence").Len() == 0 {
		t.Error("expected sanitizer to log the strip")
	}
}

// TestSanitizeToolSequence_DropsEmptiedAssistant: when the stripped
// assistant had ONLY tool_calls and no text/reasoning, dropping the
// whole message is necessary — otherwise an empty assistant message
// with an empty tool_calls array still trips OpenAI's validation
// because the next tool message has no producer.
func TestSanitizeToolSequence_DropsEmptiedAssistant(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "go"},
		}},
		// Mid-stream assistant: pure tool_call, no text. Unanswered.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ToolUseID: "X", ToolName: "read", ToolInput: "{}"},
		}},
		// Last assistant: text only — keeps the conversation alive.
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "ok done"},
		}},
	}

	result := convertMessages(msgs, "", false, nil)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (user + tail assistant), got %d", len(result))
	}
	if result[1].Role != schemas.ChatMessageRoleAssistant {
		t.Errorf("msg[1] role = %s, want assistant", result[1].Role)
	}
}
