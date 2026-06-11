package router

import (
	"context"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/provider/registry"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// captureEngine records the message passed to ProcessMessage so tests
// can assert the router converted content blocks instead of throwing
// them away.
type captureEngine struct {
	mu            sync.Mutex
	received      *types.Message
	currentImages []tool.CurrentImage
}

func (e *captureEngine) ProcessMessage(ctx context.Context, _ string, m *types.Message) (<-chan types.EngineEvent, error) {
	e.mu.Lock()
	e.received = m
	if imgs, ok := tool.CurrentImagesFromCtx(ctx); ok {
		e.currentImages = imgs
	}
	e.mu.Unlock()
	ch := make(chan types.EngineEvent)
	close(ch)
	return ch, nil
}
func (e *captureEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (e *captureEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (e *captureEngine) SubmitPlanResponse(_ context.Context, _ string, _ *types.PlanResponse) error {
	return nil
}
func (e *captureEngine) SubmitStepDecision(_ context.Context, _ string, _ *types.StepDecisionResponse) error {
	return nil
}
func (e *captureEngine) AbortSession(_ context.Context, _ string) error { return nil }

// recordingChannel collects error frames the router emits.
type recordingChannel struct {
	mu     sync.Mutex
	frames []*types.EngineEvent
}

func (c *recordingChannel) Name() string                            { return "websocket" }
func (c *recordingChannel) Start(_ context.Context) error           { return nil }
func (c *recordingChannel) Close() error                            { return nil }
func (c *recordingChannel) Health() error                           { return nil }
func (c *recordingChannel) Messages() <-chan *types.IncomingMessage { return nil }
func (c *recordingChannel) Reply(_ context.Context, _ string, msg channel.Outbound) error {
	if msg.Stream == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for evt := range msg.Stream {
		evt := evt
		c.frames = append(c.frames, &evt)
	}
	return nil
}

// stubModelInfo lets us pin a specific (key, supports) pair without
// dragging in the full registry.
type stubModelInfo struct {
	key      string
	supports registry.SupportsFlags
}

func (s stubModelInfo) ActiveModelKey() string                 { return s.key }
func (s stubModelInfo) ActiveSupports() registry.SupportsFlags { return s.supports }

// TestRouter_ConvertsImageBlocksIntoEngineMessage proves the router
// no longer drops msg.Content[]. The image block must survive into the
// engine's Message.Content.
func TestRouter_ConvertsImageBlocksIntoEngineMessage(t *testing.T) {
	eng := &captureEngine{}
	ch := &recordingChannel{}
	info := stubModelInfo{
		key:      "anthropic:claude-opus-4-7",
		supports: registry.SupportsFlags{Vision: true},
	}
	r := New(eng, map[string]channel.Duplex{"websocket": ch}, nil, info, zap.NewNop())

	err := r.Handle(context.Background(), &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   "s1",
		Content: []types.IncomingContentBlock{
			{Type: "text", Text: "what is this?"},
			{Type: "image", MIMEType: "image/png", Data: "iVBORw0KGgo="},
		},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	eng.mu.Lock()
	received := eng.received
	eng.mu.Unlock()
	if received == nil {
		t.Fatal("engine never called")
	}
	if len(received.Content) != 2 {
		t.Fatalf("want 2 blocks, got %d (%+v)", len(received.Content), received.Content)
	}
	if received.Content[1].Type != types.ContentTypeImage {
		t.Fatalf("second block wrong type: %+v", received.Content[1])
	}
	if received.Content[1].MediaType != "image/png" {
		t.Errorf("media_type lost: %+v", received.Content[1])
	}
	eng.mu.Lock()
	currentImages := eng.currentImages
	eng.mu.Unlock()
	if len(currentImages) != 1 {
		t.Fatalf("want 1 current image in tool ctx, got %d (%+v)", len(currentImages), currentImages)
	}
	if currentImages[0].MediaType != "image/png" || currentImages[0].Data != "iVBORw0KGgo=" {
		t.Fatalf("current image mismatch: %+v", currentImages[0])
	}
}

// TestRouter_RejectsImageWhenModelLacksVision is the core gate test:
// engine MUST NOT be called when the active model can't process the
// modality. Channel MUST receive a typed error frame so the UI can
// render a clear "switch model" prompt.
func TestRouter_RejectsImageWhenModelLacksVision(t *testing.T) {
	eng := &captureEngine{}
	ch := &recordingChannel{}
	info := stubModelInfo{
		key:      "anthropic:claude-haiku-4-5",
		supports: registry.SupportsFlags{}, // no Vision
	}
	r := New(eng, map[string]channel.Duplex{"websocket": ch}, nil, info, zap.NewNop())

	err := r.Handle(context.Background(), &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   "s1",
		Content: []types.IncomingContentBlock{
			{Type: "image", MIMEType: "image/png", Data: "iVBORw0KGgo="},
		},
	})
	if err == nil {
		t.Fatal("expected gate to reject")
	}
	eng.mu.Lock()
	received := eng.received
	eng.mu.Unlock()
	if received != nil {
		t.Fatal("engine must not be called when gate rejects")
	}
	ch.mu.Lock()
	frames := ch.frames
	ch.mu.Unlock()
	if len(frames) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(frames))
	}
	if frames[0].Type != types.EngineEventError {
		t.Errorf("frame type: %s", frames[0].Type)
	}
	if frames[0].Terminal == nil || frames[0].Terminal.Reason != types.TerminalUnsupportedModality {
		t.Errorf("terminal reason: %+v", frames[0].Terminal)
	}
	if frames[0].ErrorDetails == nil {
		t.Fatal("ErrorDetails missing — channel translator can't build user-facing frame")
	}
	if !strings.Contains(frames[0].Error.Error(), "claude-haiku-4-5") {
		t.Errorf("error must mention model: %v", frames[0].Error)
	}
	// Verify the rich payload survives intact.
	if rm, ok := frames[0].ErrorDetails["rejected_modalities"].([]string); !ok || len(rm) != 1 || rm[0] != "image" {
		t.Errorf("rejected_modalities malformed: %v", frames[0].ErrorDetails["rejected_modalities"])
	}
	if frames[0].ErrorDetails["user_message"] == nil {
		t.Error("user_message missing")
	}
}

// TestRouter_PassesWhenModelInfoNil ensures back-compat for tests that
// pass nil ModelInfoProvider — they should still route messages.
func TestRouter_PassesWhenModelInfoNil(t *testing.T) {
	eng := &captureEngine{}
	ch := &recordingChannel{}
	r := New(eng, map[string]channel.Duplex{"websocket": ch}, nil, nil, zap.NewNop())

	err := r.Handle(context.Background(), &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   "s1",
		Content: []types.IncomingContentBlock{
			{Type: "image", MIMEType: "image/png", Data: "AA=="},
		},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	eng.mu.Lock()
	received := eng.received
	eng.mu.Unlock()
	if received == nil {
		t.Fatal("nil ModelInfoProvider must NOT block engine dispatch")
	}
}

// TestRouter_RejectsMalformedContentWithInvalidInput exercises the
// emitInvalidInput path: Builder rejected an unsupported type → router
// emits invalid input frame, not the gate's modality frame.
func TestRouter_RejectsMalformedContentWithInvalidInput(t *testing.T) {
	eng := &captureEngine{}
	ch := &recordingChannel{}
	r := New(eng, map[string]channel.Duplex{"websocket": ch}, nil, stubModelInfo{}, zap.NewNop())

	err := r.Handle(context.Background(), &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   "s1",
		Content: []types.IncomingContentBlock{
			{Type: "video", URL: "https://x"},
		},
	})
	if err == nil {
		t.Fatal("expected Build to reject")
	}
	eng.mu.Lock()
	received := eng.received
	eng.mu.Unlock()
	if received != nil {
		t.Fatal("engine must not be called on validation error")
	}
	ch.mu.Lock()
	frames := ch.frames
	ch.mu.Unlock()
	if len(frames) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(frames))
	}
	if frames[0].Terminal == nil || frames[0].Terminal.Reason != types.TerminalModelError {
		t.Errorf("malformed input must NOT use unsupported_modality reason: %+v", frames[0].Terminal)
	}
}
