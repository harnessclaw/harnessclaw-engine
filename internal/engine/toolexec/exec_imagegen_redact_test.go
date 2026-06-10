package toolexec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type imageGenerateCaptureTool struct {
	got json.RawMessage
}

func (t *imageGenerateCaptureTool) Name() string        { return "image_generate" }
func (t *imageGenerateCaptureTool) Description() string { return "captures image input" }
func (t *imageGenerateCaptureTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *imageGenerateCaptureTool) IsReadOnly() bool                      { return false }
func (t *imageGenerateCaptureTool) IsEnabled() bool                       { return true }
func (t *imageGenerateCaptureTool) IsConcurrencySafe() bool               { return true }
func (t *imageGenerateCaptureTool) ValidateInput(_ json.RawMessage) error { return nil }
func (t *imageGenerateCaptureTool) Execute(_ context.Context, input json.RawMessage) (*types.ToolResult, error) {
	t.got = append(json.RawMessage(nil), input...)
	return &types.ToolResult{Content: "ok"}, nil
}

func TestExecutor_RedactsImageGenerateInlineDataFromVisibleToolInput(t *testing.T) {
	captureTool := &imageGenerateCaptureTool{}
	reg := tool.NewRegistry()
	if err := reg.Register(captureTool); err != nil {
		t.Fatal(err)
	}
	pool := tool.NewToolPool(reg, nil, nil)
	te := NewToolExecutor(pool, permission.BypassChecker{}, zap.NewNop(), 5*time.Second, nil)

	out := make(chan types.EngineEvent, 8)
	secret := strings.Repeat("A", 128)
	tc := types.ToolCall{
		ID:    "tu_image",
		Name:  "image_generate",
		Input: `{"prompt":"x","source_images":[{"data":"` + secret + `"}],"mask":{"b64_json":"` + secret + `"}}`,
	}

	result := te.executeSingle(context.Background(), tc, out)
	close(out)
	if result.IsError {
		t.Fatalf("executeSingle returned error: %#v", result)
	}

	var start types.EngineEvent
	for evt := range out {
		if evt.Type == types.EngineEventToolStart {
			start = evt
			break
		}
	}
	if start.Type != types.EngineEventToolStart {
		t.Fatal("missing tool_start event")
	}
	if strings.Contains(start.ToolInput, secret) {
		t.Fatalf("visible tool_input leaked image data: %s", start.ToolInput)
	}
	if !strings.Contains(start.ToolInput, "[redacted image data]") {
		t.Fatalf("visible tool_input was not redacted: %s", start.ToolInput)
	}
	if !strings.Contains(string(captureTool.got), secret) {
		t.Fatalf("tool did not receive original raw input: %s", captureTool.got)
	}
}

func TestExecutor_NormalizesImageGenerateVisibleToolInput(t *testing.T) {
	captureTool := &imageGenerateCaptureTool{}
	reg := tool.NewRegistry()
	if err := reg.Register(captureTool); err != nil {
		t.Fatal(err)
	}
	pool := tool.NewToolPool(reg, nil, nil)
	te := NewToolExecutor(pool, permission.BypassChecker{}, zap.NewNop(), 5*time.Second, nil)

	out := make(chan types.EngineEvent, 8)
	rawInput := `{"prompt":"x","model":"","source_images":[],"mask":{"path":"","url":""},"output_format":"png","output_compression":90}`
	tc := types.ToolCall{
		ID:    "tu_image",
		Name:  "image_generate",
		Input: rawInput,
	}

	result := te.executeSingle(context.Background(), tc, out)
	close(out)
	if result.IsError {
		t.Fatalf("executeSingle returned error: %#v", result)
	}

	var start types.EngineEvent
	for evt := range out {
		if evt.Type == types.EngineEventToolStart {
			start = evt
			break
		}
	}
	if start.Type != types.EngineEventToolStart {
		t.Fatal("missing tool_start event")
	}
	for _, hidden := range []string{"\"model\"", "\"source_images\"", "\"mask\"", "\"output_compression\""} {
		if strings.Contains(start.ToolInput, hidden) {
			t.Fatalf("visible tool_input still contains %s: %s", hidden, start.ToolInput)
		}
	}
	if string(captureTool.got) != rawInput {
		t.Fatalf("tool did not receive original raw input: %s", captureTool.got)
	}
}
