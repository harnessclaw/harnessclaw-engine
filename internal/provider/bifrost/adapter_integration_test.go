//go:build integration

package bifrost

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// ==============================
// Config loading
// ==============================

type bifrostTestConfig struct {
	LLM struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"llm"`
}

func loadBifrostAdapter(t *testing.T) *Adapter {
	t.Helper()
	paths := []string{"../../../testdata/llm.yaml", "testdata/llm.yaml"}
	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}

	var cfg bifrostTestConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse llm config: %v", err)
	}

	logger, _ := zap.NewDevelopment()
	adapter, err := New(Config{
		Provider: schemas.Anthropic,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
		BaseURL:  cfg.LLM.BaseURL,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("create bifrost adapter: %v", err)
	}
	t.Cleanup(func() { adapter.Shutdown() })
	return adapter
}

// drainStream reads all events from a ChatStream until closed or timeout.
func drainStream(t *testing.T, stream *provider.ChatStream, timeout time.Duration) (text string, usage *types.Usage, stopReason string, events []types.StreamEvent) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case evt, ok := <-stream.Events:
			if !ok {
				return
			}
			events = append(events, evt)
			switch evt.Type {
			case types.StreamEventText:
				text += evt.Text
			case types.StreamEventMessageEnd:
				usage = evt.Usage
				stopReason = evt.StopReason
			case types.StreamEventError:
				t.Logf("stream error: %v", evt.Error)
			}
		case <-timer.C:
			t.Fatalf("timeout (%v) draining stream, collected %d events, text=%q", timeout, len(events), text)
		}
	}
}

// ==============================
// Test 1: 简单文本对话
// ==============================

func TestIntegration_Bifrost_SimpleChat(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := adapter.Chat(ctx, &provider.ChatRequest{
		System:    "You are a helpful assistant. Be extremely concise.",
		MaxTokens: 128,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Say exactly: Hello from Bifrost!"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	text, usage, stopReason, events := drainStream(t, stream, 30*time.Second)
	t.Logf("response: %q", text)
	t.Logf("stop_reason: %s, events: %d", stopReason, len(events))

	if !strings.Contains(text, "Hello") && !strings.Contains(text, "Bifrost") {
		t.Errorf("expected response to contain 'Hello' or 'Bifrost', got %q", text)
	}
	if stopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", stopReason)
	}
	if usage == nil {
		t.Error("expected usage data")
	} else {
		t.Logf("usage: input=%d output=%d cache_read=%d cache_write=%d",
			usage.InputTokens, usage.OutputTokens, usage.CacheRead, usage.CacheWrite)
		if usage.InputTokens == 0 {
			t.Error("expected InputTokens > 0")
		}
		if usage.OutputTokens == 0 {
			t.Error("expected OutputTokens > 0")
		}
	}
	if stream.Err() != nil {
		t.Errorf("unexpected stream error: %v", stream.Err())
	}
}

// ==============================
// Test 2: 流式事件完整性
// ==============================

func TestIntegration_Bifrost_StreamEvents(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := adapter.Chat(ctx, &provider.ChatRequest{
		System:    "You are helpful.",
		MaxTokens: 256,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "Count from 1 to 5, each on a new line."}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	text, usage, stopReason, events := drainStream(t, stream, 30*time.Second)
	t.Logf("response (%d chars): %q", len(text), text)

	// Verify we got multiple text events (streaming, not batched).
	textEvents := 0
	messageEndEvents := 0
	for _, evt := range events {
		switch evt.Type {
		case types.StreamEventText:
			textEvents++
		case types.StreamEventMessageEnd:
			messageEndEvents++
		}
	}
	t.Logf("text_events=%d message_end_events=%d total_events=%d", textEvents, messageEndEvents, len(events))

	if textEvents < 2 {
		t.Errorf("expected multiple text events (streaming), got %d", textEvents)
	}
	if messageEndEvents != 1 {
		t.Errorf("expected exactly 1 message_end event, got %d", messageEndEvents)
	}

	// Verify content.
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		if !strings.Contains(text, n) {
			t.Errorf("expected %q in response", n)
		}
	}
	if stopReason != "end_turn" {
		t.Errorf("expected 'end_turn', got %q", stopReason)
	}
	if usage == nil {
		t.Error("expected usage")
	}
}

// ==============================
// Test 3: 带工具定义的请求
// ==============================

func TestIntegration_Bifrost_WithTools(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream, err := adapter.Chat(ctx, &provider.ChatRequest{
		System:    "You are helpful. When asked about the weather, always use the get_weather tool.",
		MaxTokens: 512,
		Tools: []provider.ToolSchema{
			{
				Name:        "get_weather",
				Description: "Get the current weather for a city",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string", "description": "city name"},
					},
					"required": []string{"city"},
				},
			},
		},
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "What's the weather in Tokyo?"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}

	text, _, stopReason, events := drainStream(t, stream, 60*time.Second)
	t.Logf("response text: %q", text)
	t.Logf("stop_reason: %s, events: %d", stopReason, len(events))

	// The model should call the tool → stop_reason = "tool_use".
	var toolUseEvents []types.StreamEvent
	for _, evt := range events {
		if evt.Type == types.StreamEventToolUse {
			toolUseEvents = append(toolUseEvents, evt)
		}
	}

	if len(toolUseEvents) == 0 {
		// Some models may not always call the tool; log but don't hard fail.
		t.Logf("WARNING: no tool_use events (model may have answered directly)")
		return
	}

	tc := toolUseEvents[0].ToolCall
	t.Logf("tool call: id=%s name=%s input=%s", tc.ID, tc.Name, tc.Input)

	if tc.Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", tc.Name)
	}
	if tc.ID == "" {
		t.Error("expected non-empty tool call ID")
	}
	if !strings.Contains(strings.ToLower(tc.Input), "tokyo") {
		t.Errorf("expected 'tokyo' in tool input, got %q", tc.Input)
	}
	if stopReason != "tool_use" {
		t.Errorf("expected stop_reason 'tool_use', got %q", stopReason)
	}
}

// ==============================
// Test 4: 多轮对话（工具调用 → 工具结果 → 最终回复）
// ==============================

func TestIntegration_Bifrost_ToolRoundTrip(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tools := []provider.ToolSchema{
		{
			Name:        "add",
			Description: "Adds two numbers and returns the sum",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number", "description": "first number"},
					"b": map[string]any{"type": "number", "description": "second number"},
				},
				"required": []string{"a", "b"},
			},
		},
	}

	// Turn 1: user asks, model should call the tool.
	stream1, err := adapter.Chat(ctx, &provider.ChatRequest{
		System:    "You are a calculator assistant. Always use the add tool for arithmetic.",
		MaxTokens: 512,
		Tools:     tools,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "What is 17 + 25? Use the add tool."}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Turn 1 Chat failed: %v", err)
	}

	text1, _, stopReason1, events1 := drainStream(t, stream1, 60*time.Second)
	t.Logf("turn 1: text=%q stop=%s events=%d", text1, stopReason1, len(events1))

	// Find the tool call.
	var toolCall *types.ToolCall
	for _, evt := range events1 {
		if evt.Type == types.StreamEventToolUse && evt.ToolCall != nil {
			toolCall = evt.ToolCall
			break
		}
	}
	if toolCall == nil {
		t.Logf("WARNING: model did not call tool, skipping round-trip test")
		return
	}
	t.Logf("tool call: id=%s name=%s input=%s", toolCall.ID, toolCall.Name, toolCall.Input)

	// Turn 2: send tool result back, model should produce final answer.
	stream2, err := adapter.Chat(ctx, &provider.ChatRequest{
		System:    "You are a calculator assistant. Always use the add tool for arithmetic.",
		MaxTokens: 256,
		Tools:     tools,
		Messages: []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "What is 17 + 25? Use the add tool."}},
			},
			{
				Role: types.RoleAssistant,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ToolUseID: toolCall.ID, ToolName: toolCall.Name, ToolInput: toolCall.Input},
				},
			},
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolResult, ToolUseID: toolCall.ID, ToolResult: "42"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Turn 2 Chat failed: %v", err)
	}

	text2, _, stopReason2, _ := drainStream(t, stream2, 60*time.Second)
	t.Logf("turn 2: text=%q stop=%s", text2, stopReason2)

	if !strings.Contains(text2, "42") {
		t.Errorf("expected '42' in final response, got %q", text2)
	}
	if stopReason2 != "end_turn" {
		t.Errorf("expected 'end_turn', got %q", stopReason2)
	}
}

// ==============================
// Test 5: SetModel 动态切换
// ==============================

func TestIntegration_Bifrost_SetModel(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	// Verify Name().
	if adapter.Name() != "bifrost" {
		t.Errorf("expected name 'bifrost', got %q", adapter.Name())
	}

	// Verify initial model.
	initial := adapter.currentModel()
	t.Logf("initial model: %s", initial)

	// SetModel and verify.
	adapter.SetModel("custom-model-xyz")
	if adapter.currentModel() != "custom-model-xyz" {
		t.Errorf("expected 'custom-model-xyz', got %q", adapter.currentModel())
	}

	// Reset and verify fallback to default.
	adapter.SetModel("")
	if adapter.currentModel() != initial {
		t.Errorf("expected %q after reset, got %q", initial, adapter.currentModel())
	}
}

// ==============================
// Test 6: CountTokens
// ==============================

func TestIntegration_Bifrost_CountTokens(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "This is a test message with some content for token estimation."},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeText, Text: "Here is a response."},
		}},
	}

	tokens, err := adapter.CountTokens(context.Background(), msgs)
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}
	t.Logf("estimated tokens: %d", tokens)

	// "This is a test message with some content for token estimation." = 62 chars
	// "Here is a response." = 19 chars
	// Total = 81 chars / 4 ≈ 20
	if tokens < 10 || tokens > 40 {
		t.Errorf("expected token estimate between 10-40, got %d", tokens)
	}
}

// ==============================
// Test 7: Shutdown 幂等性
// ==============================

func TestIntegration_Bifrost_ShutdownIdempotent(t *testing.T) {
	paths := []string{"../../../testdata/llm.yaml", "testdata/llm.yaml"}
	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}

	var cfg bifrostTestConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse llm config: %v", err)
	}

	adapter, err := New(Config{
		Provider: schemas.Anthropic,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
		BaseURL:  cfg.LLM.BaseURL,
	})
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}

	// Call Shutdown multiple times — should not panic.
	adapter.Shutdown()
	adapter.Shutdown()
	adapter.Shutdown()

	// After shutdown, Chat should fail gracefully (not panic).
	_, chatErr := adapter.Chat(context.Background(), &provider.ChatRequest{
		MaxTokens: 64,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hi"}}},
		},
	})
	if chatErr == nil {
		t.Log("NOTE: Chat after Shutdown did not error (SDK may allow it)")
	} else {
		t.Logf("Chat after Shutdown error (expected): %v", chatErr)
	}
}

// ==============================
// Test 8: Provider 接口合规性
// ==============================

func TestIntegration_Bifrost_ProviderInterface(t *testing.T) {
	adapter := loadBifrostAdapter(t)

	// Compile-time check: *Adapter implements provider.Provider
	var _ provider.Provider = adapter

	_ = fmt.Sprintf("provider name: %s", adapter.Name())
}
