package queryloop

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/engine/spawn"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	provretry "harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeClientRoutedTool implements tool.Tool + tool.ClientRoutedTool. We
// only need the methods executeClientTools touches: Name, IsClientRouted,
// and the Tool interface skeleton. Everything else can be a stub since
// executeClientTools never calls Execute on a client-routed tool.
type fakeClientRoutedTool struct{}

func (fakeClientRoutedTool) Name() string                              { return "ask_user_question" }
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

func (fakeDelegatedTool) Name() string                          { return "bash" }
func (fakeDelegatedTool) Description() string                   { return "" }
func (fakeDelegatedTool) IsReadOnly() bool                      { return false }
func (fakeDelegatedTool) IsConcurrencySafe() bool               { return false }
func (fakeDelegatedTool) IsEnabled() bool                       { return true }
func (fakeDelegatedTool) InputSchema() map[string]any           { return map[string]any{} }
func (fakeDelegatedTool) ValidateInput(_ json.RawMessage) error { return nil }
func (fakeDelegatedTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "ok"}, nil
}

// dispatchFakeDeps is a minimal Deps implementation sufficient for
// driving Runner.ExecuteClientTools in isolation. Only the fields
// touched by executeClientTools (Logger, LoopConfig, SessionMgr) need
// to be real — everything else is a placeholder so the type satisfies
// the Deps surface.
type dispatchFakeDeps struct {
	logger      *zap.Logger
	sessionMgr  *session.Manager
	toolTimeout time.Duration
}

func newDispatchFakeDeps(mgr *session.Manager, toolTimeout time.Duration) *dispatchFakeDeps {
	return &dispatchFakeDeps{
		logger:      zap.NewNop(),
		sessionMgr:  mgr,
		toolTimeout: toolTimeout,
	}
}

// --- Deps satisfaction (mostly stubs) ---

func (f *dispatchFakeDeps) Logger() *zap.Logger        { return f.logger }
func (f *dispatchFakeDeps) EventBus() *event.Bus       { return event.NewBus() }
func (f *dispatchFakeDeps) SessionMgr() *session.Manager { return f.sessionMgr }
func (f *dispatchFakeDeps) Spawner() *spawn.Spawner    { return nil }

func (f *dispatchFakeDeps) RegisterCancel(_ string, _ context.CancelFunc) {}
func (f *dispatchFakeDeps) DeregisterCancel(_ string)                     {}

func (f *dispatchFakeDeps) PromptBuilder() *prompt.Builder        { return nil }
func (f *dispatchFakeDeps) PromptProfile() *prompt.AgentProfile   { return nil }
func (f *dispatchFakeDeps) SkillReader() *skill.Reader            { return nil }
func (f *dispatchFakeDeps) DefRegistry() *agent.AgentDefinitionRegistry {
	return agent.NewAgentDefinitionRegistry()
}
func (f *dispatchFakeDeps) StatsRegistry() *sessionstats.Registry { return nil }
func (f *dispatchFakeDeps) CmdRegistry() *command.Registry        { return nil }
func (f *dispatchFakeDeps) Registry() *tool.Registry              { return tool.NewRegistry() }

func (f *dispatchFakeDeps) PromptConfig() PromptConfig {
	return PromptConfig{}
}

func (f *dispatchFakeDeps) LoopConfig() LoopConfig {
	return LoopConfig{ToolTimeout: f.toolTimeout, ClientTools: true}
}

func (f *dispatchFakeDeps) ContextWindow() int          { return 200000 }
func (f *dispatchFakeDeps) Provider() provider.Provider { return nil }
func (f *dispatchFakeDeps) Retryer() *provretry.Retryer { return nil }
func (f *dispatchFakeDeps) Compactor() compact.Compactor { return nil }
func (f *dispatchFakeDeps) PermChecker() permission.Checker { return nil }
func (f *dispatchFakeDeps) AgentRegistry() *agent.AgentRegistry { return nil }
func (f *dispatchFakeDeps) MessageBroker() *agent.MessageBroker { return nil }

// newDispatchTestRunner builds a Runner + Session sufficient for
// exercising executeClientTools — only the fields that branch touches
// matter. The returned session is registered with the session manager
// so a SubmitToolResult-style call (Awaits.ResolveTool) can resolve it.
func newDispatchTestRunner(t *testing.T, toolTimeout time.Duration) (*Runner, *session.Session) {
	t.Helper()
	store := memory.New()
	mgr := session.NewManager(store, zap.NewNop(), time.Hour)
	sess, err := mgr.GetOrCreate(context.Background(), "test_sid", "ws", "user_1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	deps := newDispatchFakeDeps(mgr, toolTimeout)
	runner := NewRunner(deps)
	return runner, sess
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

// stubRegistry wraps a single tool into a tool.Registry instance.
func stubRegistry(tools ...tool.Tool) *tool.Registry {
	r := tool.NewRegistry()
	for _, tl := range tools {
		_ = r.Register(tl)
	}
	return r
}

func TestExecuteClientTools_HumanInteractiveWaitsPastTimeout(t *testing.T) {
	// Arrange: a tool timeout much shorter than the time we'll let pass
	// before the user "answers". Without the human-interactive override,
	// the call would time out at toolTimeout. With the override, it
	// must wait until the user's result is delivered.
	runner, sess := newDispatchTestRunner(t, 150*time.Millisecond)
	pool := tool.NewToolPool(stubRegistry(fakeClientRoutedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	calls := []types.ToolCall{
		{ID: "toolu_user_1", Name: "ask_user_question", Input: `{"question":"x"}`},
	}

	var got []types.ToolResult
	var wg sync.WaitGroup
	wg.Add(1)
	start := time.Now()
	go func() {
		defer wg.Done()
		got = runner.ExecuteClientTools(context.Background(), sess, pool, calls, out)
	}()

	// Wait LONGER than toolTimeout to prove the timeout doesn't fire.
	// Then deliver the result the way SubmitToolResult would — via
	// the session's Awaits registry, which is the only state
	// SubmitToolResult touches besides the session lookup.
	time.Sleep(400 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("test setup wrong: only %v elapsed before delivering result", elapsed)
	}
	if err := sess.Awaits.ResolveTool(&types.ToolResultPayload{
		ToolUseID: "toolu_user_1",
		Status:    "success",
		Output:    "user picked option A",
	}); err != nil {
		t.Fatalf("ResolveTool: %v", err)
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
	runner, sess := newDispatchTestRunner(t, 80*time.Millisecond)
	pool := tool.NewToolPool(stubRegistry(fakeDelegatedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	calls := []types.ToolCall{
		{ID: "toolu_bash_1", Name: "bash", Input: `{"command":"sleep 99"}`},
	}

	start := time.Now()
	got := runner.ExecuteClientTools(context.Background(), sess, pool, calls, out)
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
	runner, sess := newDispatchTestRunner(t, 10*time.Second)
	pool := tool.NewToolPool(stubRegistry(fakeClientRoutedTool{}), nil, nil)

	out := make(chan types.EngineEvent, 8)
	stop := make(chan struct{})
	go drainEvents(out, stop)
	defer close(stop)

	ctx, cancel := context.WithCancel(context.Background())
	calls := []types.ToolCall{
		{ID: "toolu_user_2", Name: "ask_user_question", Input: `{"question":"x"}`},
	}

	var got []types.ToolResult
	done := make(chan struct{})
	go func() {
		got = runner.ExecuteClientTools(ctx, sess, pool, calls, out)
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
