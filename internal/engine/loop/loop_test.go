package loop_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/permission"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	browsertools "harnessclaw-go/internal/tools/builtin/browser"
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

	hook := func(snap loop.TurnSnapshot) loop.Decision {
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: snap.Turn,
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
	hook := func(snap loop.TurnSnapshot) loop.Decision {
		hookCalls++
		if snap.Turn == 1 {
			return loop.Decision{Inject: []types.Message{{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText, Text: "correction-injected",
				}},
			}}}
		}
		return loop.Decision{Terminate: &types.Terminal{
			Reason: types.TerminalCompleted, Turn: snap.Turn,
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

type fakeToolCallProvider struct{}

func (f *fakeToolCallProvider) Name() string { return "fake-tool-call" }
func (f *fakeToolCallProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakeToolCallProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 4)
	ch <- types.StreamEvent{
		Type: types.StreamEventToolUse,
		ToolCall: &types.ToolCall{
			ID:    "tu_browser",
			Name:  "browser_session_create",
			Input: `{"visibility":"visible"}`,
		},
	}
	ch <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: "tool_use",
		Usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

type fakeClientRoutedTool struct {
	tool.BaseTool
}

func (t *fakeClientRoutedTool) Name() string            { return "browser_session_create" }
func (t *fakeClientRoutedTool) Description() string     { return "client routed browser session" }
func (t *fakeClientRoutedTool) IsReadOnly() bool        { return false }
func (t *fakeClientRoutedTool) IsConcurrencySafe() bool { return true }
func (t *fakeClientRoutedTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (t *fakeClientRoutedTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "should not execute server-side", IsError: true}, nil
}
func (t *fakeClientRoutedTool) IsClientRouted() bool { return true }

type fakeBrowserCreateThenCommandProvider struct{}

func (f *fakeBrowserCreateThenCommandProvider) Name() string { return "fake-browser-create-command" }
func (f *fakeBrowserCreateThenCommandProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakeBrowserCreateThenCommandProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 8)
	ch <- types.StreamEvent{
		Type: types.StreamEventToolUse,
		ToolCall: &types.ToolCall{
			ID:    "tu_create",
			Name:  "browser_session_create",
			Input: `{"visibility":"hidden"}`,
		},
	}
	ch <- types.StreamEvent{
		Type: types.StreamEventToolUse,
		ToolCall: &types.ToolCall{
			ID:    "tu_command",
			Name:  "agent_browser_command",
			Input: `{"args":["snapshot"]}`,
		},
	}
	ch <- types.StreamEvent{
		Type:       types.StreamEventMessageEnd,
		StopReason: "tool_use",
		Usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

type recordingBrowserRunner struct {
	args []string
	out  []byte
}

func (r *recordingBrowserRunner) Run(_ context.Context, args []string) ([]byte, error) {
	r.args = append([]string(nil), args...)
	return r.out, nil
}

func TestRun_RoutesClientRoutedToolsThroughSessionAwaits(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	rootSess, _ := mgr.GetOrCreate(context.Background(), "root_client", "ws", "u")
	subSess, _ := mgr.GetOrCreate(context.Background(), "sub_client", "subagent", "u")
	subSess.AddMessage(types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "open browser"}}})

	reg := tool.NewRegistry()
	if err := reg.Register(&fakeClientRoutedTool{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	out := make(chan types.EngineEvent, 16)
	done := make(chan *loop.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := loop.Run(context.Background(), &loop.Config{
			Session:            subSess,
			ClientAwaitSession: rootSess,
			SystemPrompt:       "x",
			Tools:              tool.NewToolPool(reg, nil, nil),
			Provider:           &fakeToolCallProvider{},
			Retryer:            retry.New(retry.DefaultConfig(), zap.NewNop()),
			Logger:             zap.NewNop(),
			MaxTurns:           1,
			MaxTokens:          100,
			ContextWindow:      200000,
			Out:                out,
			AgentID:            "a_client",
			PermChecker:        permission.BypassChecker{},
			OnTurnComplete: func(snap loop.TurnSnapshot) loop.Decision {
				if len(snap.ToolResults) != 1 || snap.ToolResults[0].Content != "browser ready" {
					t.Errorf("tool results = %+v, want browser ready", snap.ToolResults)
				}
				if got := snap.ToolResults[0].Metadata["cdp_endpoint"]; got != "ws://127.0.0.1:9222/devtools/page/1" {
					t.Errorf("tool result metadata cdp_endpoint = %#v", got)
				}
				return loop.Decision{Terminate: &types.Terminal{Reason: types.TerminalCompleted, Turn: snap.Turn}}
			},
		})
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	for {
		select {
		case err := <-errs:
			t.Fatalf("Run error: %v", err)
		case res := <-done:
			t.Fatalf("loop finished before client tool result: %+v", res)
		case ev := <-out:
			if ev.Type != types.EngineEventToolCall {
				continue
			}
			if ev.ToolName != "browser_session_create" || ev.ToolUseID != "tu_browser" {
				t.Fatalf("unexpected tool_call event: %+v", ev)
			}
			if err := rootSess.Awaits.ResolveTool(&types.ToolResultPayload{
				ToolUseID: ev.ToolUseID,
				Status:    "success",
				Output:    "browser ready",
				Metadata: map[string]any{
					"session_id":                 "browser_session_123",
					"active_tab_id":              "tab_1",
					"agent_browser_session_name": "harnessclaw-browser-browser_session_123",
					"cdp_endpoint":               "ws://127.0.0.1:9222/devtools/page/1",
				},
			}); err != nil {
				t.Fatalf("ResolveTool: %v", err)
			}
			goto waitDone
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for client-routed tool_call")
		}
	}

waitDone:
	select {
	case err := <-errs:
		t.Fatalf("Run error: %v", err)
	case res := <-done:
		if res.Terminal.Reason != types.TerminalCompleted {
			t.Fatalf("terminal = %+v", res.Terminal)
		}
		for _, msg := range subSess.GetMessages() {
			for _, block := range msg.Content {
				if block.Type == types.ContentTypeToolResult && strings.Contains(block.ToolResult, "cdp_endpoint") {
					t.Fatalf("tool result metadata leaked into model context: %q", block.ToolResult)
				}
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for loop completion")
	}
}

func TestRun_UpdatesBrowserBindingBeforeFollowingServerTool(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	rootSess, _ := mgr.GetOrCreate(context.Background(), "root_browser_order", "ws", "u")
	subSess, _ := mgr.GetOrCreate(context.Background(), "sub_browser_order", "subagent", "u")
	subSess.AddMessage(types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "open browser"}}})

	cfg := config.BrowserAgentConfig{Enabled: true, CLITimeout: time.Second}
	runner := &recordingBrowserRunner{out: []byte(`{"success":true,"data":{"snapshot":"ok"}}`)}
	reg := tool.NewRegistry()
	if err := reg.Register(browsertools.NewSessionCreateTool(cfg)); err != nil {
		t.Fatalf("Register create: %v", err)
	}
	if err := reg.Register(browsertools.NewAgentBrowserCommandTool(cfg, runner)); err != nil {
		t.Fatalf("Register command: %v", err)
	}

	binding := browsertools.NewTaskBinding("browser_task")
	ctx := browsertools.WithTaskBinding(context.Background(), binding)
	out := make(chan types.EngineEvent, 32)
	done := make(chan *loop.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := loop.Run(ctx, &loop.Config{
			Session:            subSess,
			ClientAwaitSession: rootSess,
			SystemPrompt:       "x",
			Tools:              tool.NewToolPool(reg, nil, nil),
			Provider:           &fakeBrowserCreateThenCommandProvider{},
			Retryer:            retry.New(retry.DefaultConfig(), zap.NewNop()),
			Logger:             zap.NewNop(),
			MaxTurns:           1,
			MaxTokens:          100,
			ContextWindow:      200000,
			Out:                out,
			AgentID:            "a_browser_order",
			PermChecker:        permission.BypassChecker{},
			Hooks: loop.Hooks{
				OnToolResult: func(_ int, call types.ToolCall, result types.ToolResult) {
					browsertools.UpdateTaskBindingFromToolResult(call.Name, result, binding)
				},
			},
			OnTurnComplete: func(snap loop.TurnSnapshot) loop.Decision {
				if len(snap.ToolResults) != 2 {
					t.Errorf("tool results len = %d, want 2: %+v", len(snap.ToolResults), snap.ToolResults)
				} else if snap.ToolResults[1].IsError {
					t.Errorf("agent_browser_command result = %+v, want success", snap.ToolResults[1])
				}
				return loop.Decision{Terminate: &types.Terminal{Reason: types.TerminalCompleted, Turn: snap.Turn}}
			},
		})
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	for {
		select {
		case err := <-errs:
			t.Fatalf("Run error: %v", err)
		case res := <-done:
			t.Fatalf("loop finished before client tool result: %+v", res)
		case ev := <-out:
			if ev.Type != types.EngineEventToolCall {
				continue
			}
			if ev.ToolName != "browser_session_create" || ev.ToolUseID != "tu_create" {
				t.Fatalf("unexpected client tool_call event: %+v", ev)
			}
			if err := rootSess.Awaits.ResolveTool(&types.ToolResultPayload{
				ToolUseID: ev.ToolUseID,
				Status:    "success",
				Output:    `{"session_id":"browser_session_123","window_id":"1"}`,
				Metadata: map[string]any{
					"session_id":                 "browser_session_123",
					"active_tab_id":              "tab_1",
					"agent_browser_session_name": "harnessclaw-browser-browser_session_123",
					"cdp_endpoint":               "ws://127.0.0.1:62957/devtools/page/page-1",
				},
			}); err != nil {
				t.Fatalf("ResolveTool: %v", err)
			}
			goto waitDone
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for client-routed tool_call")
		}
	}

waitDone:
	select {
	case err := <-errs:
		t.Fatalf("Run error: %v", err)
	case res := <-done:
		if res.Terminal.Reason != types.TerminalCompleted {
			t.Fatalf("terminal = %+v", res.Terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for loop completion")
	}

	wantArgs := []string{
		"--session", "harnessclaw-browser-browser_session_123",
		"--cdp", "ws://127.0.0.1:62957/devtools/page/page-1",
		"--json",
		"snapshot",
	}
	if strings.Join(runner.args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("runner args = %v, want %v", runner.args, wantArgs)
	}
}

func TestRun_MaxTurnsHit(t *testing.T) {
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	sess, _ := mgr.GetOrCreate(context.Background(), "t3", "ws", "u")
	sess.AddMessage(types.Message{Role: types.RoleUser, Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "go"}}})

	hook := func(_ loop.TurnSnapshot) loop.Decision {
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
