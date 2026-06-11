package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	schedpkg "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// mockScheduler records the SpawnParams passed to Dispatch.
type mockScheduler struct {
	params *schedpkg.SpawnParams
	result schedpkg.Result
	err    error
}

func (m *mockScheduler) Dispatch(_ context.Context, p schedpkg.SpawnParams) (schedpkg.Result, error) {
	copied := p
	m.params = &copied
	return m.result, m.err
}

func (m *mockScheduler) Subscribe(context.Context, types.TaskID) (<-chan types.EngineEvent, error) {
	return nil, schedpkg.ErrNotSubscribable
}

func newSched(result schedpkg.Result, err error) *mockScheduler {
	return &mockScheduler{result: result, err: err}
}

func newToolForTest() *Tool {
	return New(&mockScheduler{}, definition.NewRegistry(), zap.NewNop())
}

func TestTool_Metadata(t *testing.T) {
	tl := newToolForTest()
	if tl.Name() != ToolName {
		t.Fatalf("Name = %q", tl.Name())
	}
	if !tl.IsLongRunning() {
		t.Error("scheduler must be long-running")
	}
	if tl.IsConcurrencySafe() {
		t.Error("scheduler must not be concurrency-safe")
	}
}

func TestTool_ValidateInput(t *testing.T) {
	tl := newToolForTest()
	if err := tl.ValidateInput(json.RawMessage(`{"task":"do thing"}`)); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{}`)); err == nil {
		t.Error("empty task should be rejected")
	}
}

func TestTool_Execute_SyncSuccess(t *testing.T) {
	sched := newSched(schedpkg.Result{
		AgentID: "a-1",
		TaskID:  "t-1",
		Outcome: schedpkg.SyncOutcome{
			Content:  []types.ContentBlock{{Type: types.ContentTypeText, Text: "scheduler summary"}},
			Terminal: types.Terminal{Reason: types.TerminalCompleted},
		},
	}, nil)
	tl := New(sched, definition.NewRegistry(), zap.NewNop())

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"task":"deep dive"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if res.Content != "scheduler summary" {
		t.Errorf("Content = %q", res.Content)
	}
	if sched.params == nil {
		t.Fatal("Dispatch was not called")
	}
	if sched.params.Definition.Name != SubagentType {
		t.Errorf("Definition.Name = %q want %q", sched.params.Definition.Name, SubagentType)
	}
	if sched.params.Prompt != "deep dive" {
		t.Errorf("Prompt = %q", sched.params.Prompt)
	}
}

func TestTool_Execute_PlanModeReturnsError(t *testing.T) {
	tl := newToolForTest()
	ctx := tool.WithCoordinatorMode(context.Background(), "plan")
	res, err := tl.Execute(ctx, json.RawMessage(`{"task":"x"}`))
	if err != nil {
		t.Fatalf("Execute returned go err: %v", err)
	}
	if !res.IsError {
		t.Fatal("plan mode should produce IsError=true")
	}
	if !strings.Contains(res.Content, "plan") || !strings.Contains(res.Content, "unavailable") {
		t.Errorf("Content should explain plan unavailable: %q", res.Content)
	}
}

func TestTool_Execute_DispatchError(t *testing.T) {
	tl := New(newSched(schedpkg.Result{}, errors.New("dispatch boom")), definition.NewRegistry(), zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if !res.IsError {
		t.Fatal("expected IsError=true on dispatch error")
	}
	if !strings.Contains(res.Content, "dispatch boom") {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestTool_Execute_TerminalFailure(t *testing.T) {
	sched := newSched(schedpkg.Result{
		AgentID: "a-x",
		Outcome: schedpkg.SyncOutcome{
			Terminal: types.Terminal{Reason: types.TerminalMaxTurns, Message: "out of turns"},
		},
	}, nil)
	tl := New(sched, definition.NewRegistry(), zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x"}`))
	if !res.IsError {
		t.Fatal("non-completed Terminal should be IsError=true")
	}
	if !strings.Contains(res.Content, "max_turns") {
		t.Errorf("Content should include terminal reason: %q", res.Content)
	}
}
