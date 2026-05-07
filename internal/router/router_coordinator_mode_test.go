package router

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/channel"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// modeRecordingEngine captures the ctx its ProcessMessage receives so the
// test can assert tool.GetCoordinatorMode picks up what the router put
// there. Implements the engine.Engine interface used by Router.
type modeRecordingEngine struct {
	gotMode string
}

func (m *modeRecordingEngine) ProcessMessage(ctx context.Context, _ string, _ *types.Message) (<-chan types.EngineEvent, error) {
	m.gotMode = tool.GetCoordinatorMode(ctx)
	out := make(chan types.EngineEvent)
	close(out)
	return out, nil
}

func (m *modeRecordingEngine) SubmitToolResult(_ context.Context, _ string, _ *types.ToolResultPayload) error {
	return nil
}
func (m *modeRecordingEngine) SubmitPermissionResult(_ context.Context, _ string, _ *types.PermissionResponse) error {
	return nil
}
func (m *modeRecordingEngine) SubmitPlanResponse(_ context.Context, _ string, _ *types.PlanResponse) error {
	return nil
}
func (m *modeRecordingEngine) AbortSession(_ context.Context, _ string) error { return nil }

// noopChannel implements channel.Channel; SendEvent never gets called in
// these tests because ProcessMessage returns an immediately-closed
// channel.
type noopChannel struct{}

func (noopChannel) Name() string                                                       { return "noop" }
func (noopChannel) Start(_ context.Context, _ channel.MessageHandler) error            { return nil }
func (noopChannel) Stop(_ context.Context) error                                       { return nil }
func (noopChannel) Send(_ context.Context, _ string, _ *types.Message) error           { return nil }
func (noopChannel) SendEvent(_ context.Context, _ string, _ *types.EngineEvent) error  { return nil }
func (noopChannel) Health() error                                                      { return nil }

func TestRouter_AttachesCoordinatorModeToCtx(t *testing.T) {
	eng := &modeRecordingEngine{}
	r := New(eng, map[string]channel.Channel{"noop": noopChannel{}}, nil, zap.NewNop())

	msg := &types.IncomingMessage{
		ChannelName:     "noop",
		SessionID:       "sess1",
		Text:            "hi",
		CoordinatorMode: "plan",
	}
	if err := r.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if eng.gotMode != "plan" {
		t.Errorf("router did not propagate mode to ctx; got %q", eng.gotMode)
	}
}

func TestRouter_NoModeWhenIncomingHasNone(t *testing.T) {
	eng := &modeRecordingEngine{}
	r := New(eng, map[string]channel.Channel{"noop": noopChannel{}}, nil, zap.NewNop())

	msg := &types.IncomingMessage{
		ChannelName: "noop",
		SessionID:   "sess1",
		Text:        "hi",
	}
	if err := r.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if eng.gotMode != "" {
		t.Errorf("absent mode should not be invented; got %q", eng.gotMode)
	}
}

func TestRouter_ModePassesThroughUnchanged(t *testing.T) {
	// Defensive: prove the router doesn't accidentally validate or
	// rewrite the mode. The downstream registry handles unknown
	// values with a fallback policy; the router must stay agnostic.
	for _, mode := range []string{"react", "plan", "garbage", "Plan"} {
		t.Run(mode, func(t *testing.T) {
			eng := &modeRecordingEngine{}
			r := New(eng, map[string]channel.Channel{"noop": noopChannel{}}, nil, zap.NewNop())
			msg := &types.IncomingMessage{
				ChannelName:     "noop",
				SessionID:       "s",
				Text:            "x",
				CoordinatorMode: mode,
			}
			if err := r.Handle(context.Background(), msg); err != nil {
				t.Fatalf("Handle error: %v", err)
			}
			if eng.gotMode != mode {
				t.Errorf("router rewrote mode; sent %q got %q", mode, eng.gotMode)
			}
		})
	}
}
