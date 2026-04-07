package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

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
	result := convertMessages(msgs, "You are helpful.")

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
	result := convertMessages(msgs, "")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestConvertMessages_ToolResult(t *testing.T) {
	msgs := []types.Message{
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type:      types.ContentTypeToolResult,
				ToolUseID: "tool-123",
				ToolResult: "42",
			}},
		},
	}
	result := convertMessages(msgs, "")

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role != schemas.ChatMessageRoleTool {
		t.Errorf("expected tool role, got %s", result[0].Role)
	}
	if result[0].ChatToolMessage == nil || result[0].ChatToolMessage.ToolCallID == nil {
		t.Fatal("expected ChatToolMessage with ToolCallID")
	}
	if *result[0].ChatToolMessage.ToolCallID != "tool-123" {
		t.Errorf("unexpected tool call ID: %s", *result[0].ChatToolMessage.ToolCallID)
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
	result := convertMessages(msgs, "")

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
	result := convertMessages(msgs, "")
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
	params := buildParams(req)
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
	params := buildParams(req)
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
