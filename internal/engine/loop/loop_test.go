package loop_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeProvider returns a single canned stream: text "ok" + end_turn.
type fakeProvider struct{}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakeProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 4)
	ch <- types.StreamEvent{Type: types.StreamEventText, Text: "ok"}
	ch <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: "end_turn",
		Usage:      &types.Usage{InputTokens: 5, OutputTokens: 2},
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func TestRun_TerminatesWhenHookReturnsTerminal(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	sess, _ := mgr.GetOrCreate(context.Background(), "t1", "ws", "u")
	sess.AddMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}},
	})

	hook := func(turn int, msg types.Message, toolResults []types.ToolResult) loop.Decision {
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: turn,
		}}
	}

	out := make(chan types.EngineEvent, 32)
	defer close(out)

	res, err := loop.Run(context.Background(), &loop.Config{
		Session:        sess,
		SystemPrompt:   "you are a test",
		Tools:          tool.NewToolPool(tool.NewRegistry(), nil, nil),
		Provider:       &fakeProvider{},
		Retryer:        retry.New(retry.DefaultConfig(), zap.NewNop()),
		Logger:         zap.NewNop(),
		MaxTurns:       5,
		MaxTokens:      100,
		ContextWindow:  200000,
		Out:            out,
		AgentID:        "a1",
		PermChecker:    permission.BypassChecker{},
		OnTurnComplete: hook,
	})

	if err != nil {
		t.Fatalf("loop.Run error: %v", err)
	}
	if res.Terminal.Reason != types.TerminalCompleted {
		t.Errorf("Terminal.Reason = %v, want %v", res.Terminal.Reason, types.TerminalCompleted)
	}
	if res.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", res.NumTurns)
	}
}

// fakeProviderSequence returns turn-i text for turn i.
type fakeProviderSequence struct {
	calls int
}

func (f *fakeProviderSequence) Name() string { return "fake-seq" }
func (f *fakeProviderSequence) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakeProviderSequence) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	f.calls++
	text := "turn-" + string(rune('0'+f.calls))
	ch := make(chan types.StreamEvent, 4)
	ch <- types.StreamEvent{Type: types.StreamEventText, Text: text}
	ch <- types.StreamEvent{
		Type: types.StreamEventMessageEnd, StopReason: "end_turn",
		Usage: &types.Usage{InputTokens: 1, OutputTokens: 1},
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func TestRun_InjectsMessagesBeforeNextTurn(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	sess, _ := mgr.GetOrCreate(context.Background(), "t2", "ws", "u")
	sess.AddMessage(types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "go"}},
	})

	hookCalls := 0
	hook := func(turn int, msg types.Message, _ []types.ToolResult) loop.Decision {
		hookCalls++
		if turn == 1 {
			return loop.Decision{Inject: []types.Message{{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText, Text: "correction-injected",
				}},
			}}}
		}
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: turn,
		}}
	}

	out := make(chan types.EngineEvent, 64)
	defer close(out)

	res, err := loop.Run(context.Background(), &loop.Config{
		Session: sess, SystemPrompt: "x",
		Tools:    tool.NewToolPool(tool.NewRegistry(), nil, nil),
		Provider: &fakeProviderSequence{},
		Retryer:  retry.New(retry.DefaultConfig(), zap.NewNop()),
		Logger:   zap.NewNop(),
		MaxTurns: 5, MaxTokens: 100, ContextWindow: 200000,
		Out: out, AgentID: "a2",
		PermChecker:    permission.BypassChecker{},
		OnTurnComplete: hook,
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", res.NumTurns)
	}
	if hookCalls != 2 {
		t.Errorf("hookCalls = %d, want 2", hookCalls)
	}
	// Session should contain injected message between turns 1 and 2.
	msgs := sess.GetMessages()
	var foundInjection bool
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Text == "correction-injected" {
				foundInjection = true
			}
		}
	}
	if !foundInjection {
		t.Error("expected injected correction in session.messages")
	}
}

func TestRun_MaxTurnsHit(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	sess, _ := mgr.GetOrCreate(context.Background(), "t3", "ws", "u")
	sess.AddMessage(types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "go"}}})

	hook := func(_ int, _ types.Message, _ []types.ToolResult) loop.Decision {
		return loop.Decision{} // never terminate
	}
	out := make(chan types.EngineEvent, 64)
	defer close(out)

	res, _ := loop.Run(context.Background(), &loop.Config{
		Session: sess, SystemPrompt: "x",
		Tools:    tool.NewToolPool(tool.NewRegistry(), nil, nil),
		Provider: &fakeProviderSequence{},
		Retryer:  retry.New(retry.DefaultConfig(), zap.NewNop()),
		Logger:   zap.NewNop(),
		MaxTurns: 2, MaxTokens: 100, ContextWindow: 200000,
		Out: out, AgentID: "a3",
		PermChecker:    permission.BypassChecker{},
		OnTurnComplete: hook,
	})
	if res.Terminal.Reason != types.TerminalMaxTurns {
		t.Errorf("Terminal.Reason = %v, want TerminalMaxTurns", res.Terminal.Reason)
	}
	if res.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", res.NumTurns)
	}
}
