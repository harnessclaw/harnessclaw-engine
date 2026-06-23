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

// newRegistryWithFreelancer returns a Registry containing a minimal
// "freelancer" AgentDefinition so dispatch tool tests can resolve the
// subagent_type they pass in. Tests that want to verify "unknown agent"
// behaviour use definition.NewRegistry() directly.
func newRegistryWithFreelancer(t *testing.T) *definition.Registry {
	t.Helper()
	reg := definition.NewRegistry()
	if err := reg.Register(&definition.AgentDefinition{
		Name:      "freelancer",
		AgentType: tool.AgentTypeSync,
	}); err != nil {
		t.Fatalf("seed freelancer def: %v", err)
	}
	return reg
}

func newToolForTest(t *testing.T) *Tool {
	return New(&mockScheduler{}, newRegistryWithFreelancer(t), nil, zap.NewNop())
}

func TestTool_Metadata(t *testing.T) {
	tl := newToolForTest(t)
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
	tl := newToolForTest(t)
	if err := tl.ValidateInput(json.RawMessage(`{"task":"do thing","subagent_type":"freelancer"}`)); err != nil {
		t.Errorf("valid input rejected: %v", err)
	}
	if err := tl.ValidateInput(json.RawMessage(`{}`)); err == nil {
		t.Error("empty input should be rejected")
	}
	if err := tl.ValidateInput(json.RawMessage(`{"task":"do thing"}`)); err == nil {
		t.Error("missing subagent_type should be rejected")
	}
	if err := tl.ValidateInput(json.RawMessage(`{"subagent_type":"freelancer"}`)); err == nil {
		t.Error("missing task should be rejected")
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
	tl := New(sched, newRegistryWithFreelancer(t), nil, zap.NewNop())

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"task":"deep dive","subagent_type":"freelancer"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	// Content 由 concatText(sync.Content) + framework 注入的 <dispatch-meta> 尾注组成。
	if !strings.HasPrefix(res.Content, "scheduler summary") {
		t.Errorf("Content should start with sub-agent text, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "<dispatch-meta>") || !strings.Contains(res.Content, "task_id: t-1") {
		t.Errorf("Content should embed dispatch-meta with task_id, got %q", res.Content)
	}
	if sched.params == nil {
		t.Fatal("Dispatch was not called")
	}
	if sched.params.Definition.Name != "freelancer" {
		t.Errorf("Definition.Name = %q want %q", sched.params.Definition.Name, "freelancer")
	}
	if sched.params.Prompt != "deep dive" {
		t.Errorf("Prompt = %q", sched.params.Prompt)
	}
}

func TestTool_Execute_UnknownSubagentType(t *testing.T) {
	tl := New(&mockScheduler{}, newRegistryWithFreelancer(t), nil, zap.NewNop())
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"task":"do x","subagent_type":"nonexistent"}`))
	if err != nil {
		t.Fatalf("Execute returned go err: %v", err)
	}
	if !res.IsError {
		t.Fatal("unknown subagent_type should produce IsError=true")
	}
	if !strings.Contains(res.Content, "nonexistent") {
		t.Errorf("Content should name the unknown agent: %q", res.Content)
	}
}

func TestTool_Execute_PlanModeReturnsError(t *testing.T) {
	tl := newToolForTest(t)
	ctx := tool.WithCoordinatorMode(context.Background(), "plan")
	res, err := tl.Execute(ctx, json.RawMessage(`{"task":"x","subagent_type":"freelancer"}`))
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
	tl := New(newSched(schedpkg.Result{}, errors.New("dispatch boom")), newRegistryWithFreelancer(t), nil, zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x","subagent_type":"freelancer"}`))
	if !res.IsError {
		t.Fatal("expected IsError=true on dispatch error")
	}
	if !strings.Contains(res.Content, "dispatch boom") {
		t.Errorf("Content = %q", res.Content)
	}
	if res.ErrorType != types.ToolErrorDependencyFail {
		t.Errorf("real dispatch error should be DependencyFail, got %q", res.ErrorType)
	}
}

// TestTool_Execute_UserCancelled 验证用户取消（ctx 已 cancel）路径返回
// user_aborted 错误类，让前端不要显示「前置步骤失败 + 自动重试」转圈。
func TestTool_Execute_UserCancelled(t *testing.T) {
	tl := New(newSched(schedpkg.Result{}, context.Canceled), newRegistryWithFreelancer(t), nil, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟用户取消：ctx 立即结束

	res, _ := tl.Execute(ctx, json.RawMessage(`{"task":"x","subagent_type":"freelancer"}`))
	if !res.IsError {
		t.Fatal("expected IsError=true on cancellation")
	}
	if res.ErrorType != types.ToolErrorUserAborted {
		t.Errorf("user cancel should map to ToolErrorUserAborted, got %q", res.ErrorType)
	}
	if strings.Contains(res.Content, "context canceled") {
		t.Errorf("user-facing message should not leak raw 'context canceled', got %q", res.Content)
	}
}

func TestAppendDispatchMeta(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		taskID       string
		deliverables []types.Deliverable
		want         string
	}{
		{
			name:    "taskID only — no outputs",
			content: "邮件写好了。",
			taskID:  "t-ae15b37c-7d6",
			want:    "邮件写好了。\n\n<dispatch-meta>\ntask_id: t-ae15b37c-7d6\n</dispatch-meta>",
		},
		{
			name:    "taskID + outputs",
			content: "done",
			taskID:  "t-abc",
			deliverables: []types.Deliverable{
				{FilePath: "/workspace/session/sid/tasks/t-abc/report.md"},
				{FilePath: "/workspace/session/sid/tasks/t-abc/data.csv"},
			},
			want: "done\n\n<dispatch-meta>\ntask_id: t-abc\noutputs:\n  - report.md\n  - data.csv\n</dispatch-meta>",
		},
		{
			name:    "empty content still gets meta",
			content: "",
			taskID:  "t-xyz",
			want:    "\n<dispatch-meta>\ntask_id: t-xyz\n</dispatch-meta>",
		},
		{
			name:    "content already ends with newline — no double blank",
			content: "done\n",
			taskID:  "t-1",
			want:    "done\n\n<dispatch-meta>\ntask_id: t-1\n</dispatch-meta>",
		},
		{
			name:    "no taskID — no-op (defensive)",
			content: "done",
			taskID:  "",
			want:    "done",
		},
		{
			name:    "duplicate basenames deduped",
			content: "x",
			taskID:  "t-1",
			deliverables: []types.Deliverable{
				{FilePath: "/a/report.md"},
				{FilePath: "/b/report.md"},
			},
			want: "x\n\n<dispatch-meta>\ntask_id: t-1\noutputs:\n  - report.md\n</dispatch-meta>",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := appendDispatchMeta(c.content, c.taskID, c.deliverables)
			if got != c.want {
				t.Errorf("appendDispatchMeta\ngot:  %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestTool_Execute_TerminalFailure(t *testing.T) {
	sched := newSched(schedpkg.Result{
		AgentID: "a-x",
		Outcome: schedpkg.SyncOutcome{
			Terminal: types.Terminal{Reason: types.TerminalMaxTurns, Message: "out of turns"},
		},
	}, nil)
	tl := New(sched, newRegistryWithFreelancer(t), nil, zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"task":"x","subagent_type":"freelancer"}`))
	if !res.IsError {
		t.Fatal("non-completed Terminal should be IsError=true")
	}
	if !strings.Contains(res.Content, "max_turns") {
		t.Errorf("Content should include terminal reason: %q", res.Content)
	}
}
