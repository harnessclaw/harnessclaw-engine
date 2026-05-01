package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeClientRoutedTool implements tool.Tool + tool.ClientRoutedTool. We
// only need the methods executeClientTools touches: Name, IsClientRouted,
// and the Tool interface skeleton. Everything else can be a stub since
// executeClientTools never calls Execute on a client-routed tool.
type fakeClientRoutedTool struct{}

func (fakeClientRoutedTool) Name() string                              { return "AskUserQuestion" }
func (fakeClientRoutedTool) Description() string                       { return "" }
func (fakeClientRoutedTool) IsReadOnly() bool                          { return false }
func (fakeClientRoutedTool) IsConcurrencySafe() bool                   { return true }
func (fakeClientRoutedTool) IsEnabled() bool                           { return true }
func (fakeClientRoutedTool) InputSchema() map[string]any               { return map[string]any{} }
func (fakeClientRoutedTool) ValidateInput(_ json.RawMessage) error     { return nil }
func (fakeClientRoutedTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "should not be called"}, nil
}
func (fakeClientRoutedTool) IsClientRouted() bool { return true }

// fakeDelegatedTool is a non-client-routed tool used to verify the
// delegation branch keeps its timeout.
type fakeDelegatedTool struct{}

func (fakeDelegatedTool) Name() string                          { return "Bash" }
func (fakeDelegatedTool) Description() string                   { return "" }
func (fakeDelegatedTool) IsReadOnly() bool                      { return false }
func (fakeDelegatedTool) IsConcurrencySafe() bool               { return false }
func (fakeDelegatedTool) IsEnabled() bool                       { return true }
func (fakeDelegatedTool) InputSchema() map[string]any           { return map[string]any{} }
func (fakeDelegatedTool) ValidateInput(_ json.RawMessage) error { return nil }
func (fakeDelegatedTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "ok"}, nil
}

// newDispatchTestEngine builds a minimal QueryEngine sufficient for exercising
// executeClientTools — only the fields that branch touches matter.
func newDispatchTestEngine(toolTimeout time.Duration) *QueryEngine {
	return &QueryEngine{
		config:       QueryEngineConfig{ToolTimeout: toolTimeout, ClientTools: true},
		pendingTools: make(map[string]*pendingToolCall),
	}
}

// drainEvents consumes events emitted by executeClientTools so the call
// doesn't block on the unbuffered out channel.
func drainEvents(out chan types.EngineEvent, stop <-chan struct{}) {
	for {
		select {
		case <-out:
		case <-stop:
			return
		}
	}
}

func TestExecuteClientTools_HumanInteractiveWaitsPastTimeout(t *testing.T) {
	// Arrange: a tool timeout much shorter than the time we'll let pass
	// before the user "answers". Without the human-interactive override,
	// the call would time out at toolTimeout. With the override, it
	// must wait until SubmitToolResult fires.
	qe := newDispatchTestEngine(150 * time.Millisecond)
	pool := tool.NewToolPool(stubRegistry(fakeClientRoutedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	calls := []types.ToolCall{
		{ID: "toolu_user_1", Name: "AskUserQuestion", Input: `{"question":"x"}`},
	}

	var got []types.ToolResult
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	go func() {
		defer wg.Done()
		got = qe.executeClientTools(context.Background(), pool, calls, out)
	}()

	// Wait LONGER than toolTimeout to prove the timeout doesn't fire.
	// Then deliver the result the way SubmitToolResult would.
	time.Sleep(400 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("test setup wrong: only %v elapsed before delivering result", elapsed)
	}
	if err := qe.SubmitToolResult(context.Background(), "", &types.ToolResultPayload{
		ToolUseID: "toolu_user_1",
		Status:    "success",
		Output:    "user picked option A",
	}); err != nil {
		t.Fatalf("SubmitToolResult: %v", err)
	}

	wg.Wait()
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].IsError {
		t.Fatalf("human-interactive call should not error: %+v", got[0])
	}
	if got[0].Content != "user picked option A" {
		t.Fatalf("payload not delivered: got %q", got[0].Content)
	}
}

func TestExecuteClientTools_DelegatedToolStillTimesOut(t *testing.T) {
	// Sanity: a tool that is NOT ClientRouted (the Claude Code CLI
	// delegation case) keeps its timeout — otherwise a crashed client
	// would pin the engine forever.
	qe := newDispatchTestEngine(80 * time.Millisecond)
	pool := tool.NewToolPool(stubRegistry(fakeDelegatedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	calls := []types.ToolCall{
		{ID: "toolu_bash_1", Name: "Bash", Input: `{"command":"sleep 99"}`},
	}

	start := time.Now()
	got := qe.executeClientTools(context.Background(), pool, calls, out)
	elapsed := time.Since(start)

	if elapsed < 80*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("expected to time out around 80ms, got %v", elapsed)
	}
	if !got[0].IsError {
		t.Fatalf("expected timeout to surface as error, got %+v", got[0])
	}
}

func TestExecuteClientTools_HumanInteractiveCancelledByContext(t *testing.T) {
	// Even with no timeout, ctx cancellation must unblock the wait so
	// session.interrupt actually interrupts.
	qe := newDispatchTestEngine(10 * time.Second)
	pool := tool.NewToolPool(stubRegistry(fakeClientRoutedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	ctx, cancel := context.WithCancel(context.Background())
	calls := []types.ToolCall{
		{ID: "toolu_user_2", Name: "AskUserQuestion", Input: `{"question":"x"}`},
	}

	var got []types.ToolResult
	done := make(chan struct{})
	go func() {
		got = qe.executeClientTools(ctx, pool, calls, out)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ctx cancel did not unblock the wait")
	}
	if !got[0].IsError {
		t.Fatalf("cancelled call should be an error result, got %+v", got[0])
	}
}

// stubRegistry wraps a single tool into a tool.Registry instance.
// tool.NewToolPool needs a registry; we don't have a public constructor
// we can call directly here, so we fall back to the registry's Register.
func stubRegistry(tools ...tool.Tool) *tool.Registry {
	r := tool.NewRegistry()
	for _, t := range tools {
		r.Register(t)
	}
	return r
}
