package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// mockScheduler 记录 Dispatch 接收到的 params，可注入返回值或错误。
type mockScheduler struct {
	params *scheduler.SpawnParams
	result scheduler.Result
	err    error
}

func (m *mockScheduler) Dispatch(_ context.Context, p scheduler.SpawnParams) (scheduler.Result, error) {
	copied := p
	m.params = &copied
	return m.result, m.err
}

func (m *mockScheduler) Subscribe(context.Context, types.TaskID) (<-chan types.EngineEvent, error) {
	return nil, scheduler.ErrNotSubscribable
}

func newTool() *AgentTool {
	return New(&mockScheduler{}, definition.NewRegistry(), zap.NewNop())
}

func TestAgentTool_Name(t *testing.T) {
	tool := newTool()
	if tool.Name() != "freelance" {
		t.Errorf("expected name 'freelance', got %q", tool.Name())
	}
}

func TestAgentTool_IsLongRunning(t *testing.T) {
	tool := newTool()
	if !tool.IsLongRunning() {
		t.Error("expected IsLongRunning to return true")
	}
}

func TestAgentTool_IsReadOnly(t *testing.T) {
	tool := newTool()
	if tool.IsReadOnly() {
		t.Error("expected IsReadOnly to return false")
	}
}

func TestAgentTool_ValidateInput(t *testing.T) {
	tool := newTool()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid minimal", input: `{"prompt":"search for tests"}`, wantErr: false},
		{name: "valid full", input: `{"prompt":"find files","subagent_type":"plan","description":"find test files","name":"explorer"}`, wantErr: false},
		{name: "missing prompt", input: `{"subagent_type":"plan"}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tool.ValidateInput(json.RawMessage(tc.input))
			if (err != nil) != tc.wantErr {
				t.Errorf("got err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestAgentTool_Execute_SyncSuccess(t *testing.T) {
	sched := &mockScheduler{
		result: scheduler.Result{
			AgentID: "a-1",
			TaskID:  "t-1",
			Outcome: scheduler.SyncOutcome{
				Content:  []types.ContentBlock{{Type: types.ContentTypeText, Text: "done"}},
				Terminal: types.Terminal{Reason: types.TerminalCompleted},
			},
		},
	}
	tool := New(sched, definition.NewRegistry(), zap.NewNop())

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"do thing","subagent_type":"freelancer","name":"alice"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	// Content 拼了 sub-agent 文本 + framework 注入的 <dispatch-meta> 尾注。
	if !strings.HasPrefix(res.Content, "done") {
		t.Errorf("Content should start with sub-agent text %q, got %q", "done", res.Content)
	}
	if !strings.Contains(res.Content, "<dispatch-meta>") || !strings.Contains(res.Content, "task_id: t-1") {
		t.Errorf("Content should embed dispatch-meta with task_id, got %q", res.Content)
	}
	if sched.params == nil {
		t.Fatal("Dispatch was not called")
	}
	if sched.params.Definition.Name != "freelancer" {
		t.Errorf("Definition.Name = %q (resolveDefinition fallback should set Name from SubagentType)", sched.params.Definition.Name)
	}
	if sched.params.Name != "alice" {
		t.Errorf("Name = %q", sched.params.Name)
	}
	if sched.params.Hints.Background {
		t.Error("Hints.Background must be false for sync")
	}
}

func TestAgentTool_Execute_AsyncReturnsLaunched(t *testing.T) {
	sched := &mockScheduler{
		result: scheduler.Result{
			AgentID: "a-2",
			TaskID:  "t-2",
			Status:  scheduler.StatusAsyncLaunched,
			Outcome: scheduler.AsyncOutcome{OutputFile: "/tmp/t-2.jsonl", Tailable: true},
		},
	}
	tool := New(sched, definition.NewRegistry(), zap.NewNop())

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"long task","subagent_type":"freelancer","run_in_background":true}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res)
	}
	if !strings.Contains(res.Content, "a-2") {
		t.Errorf("Content missing agent_id: %q", res.Content)
	}
	if got := res.Metadata["background"]; got != true {
		t.Errorf("Metadata.background = %v want true", got)
	}
	if got := res.Metadata["task_id"]; got != types.TaskID("t-2") {
		t.Errorf("Metadata.task_id = %v", got)
	}
	if !sched.params.Hints.Background {
		t.Error("Hints.Background must be true for async path")
	}
}

func TestAgentTool_Execute_DispatchError(t *testing.T) {
	sched := &mockScheduler{err: errors.New("dispatch boom")}
	tool := New(sched, definition.NewRegistry(), zap.NewNop())

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x","subagent_type":"freelancer"}`))
	if err != nil {
		t.Fatalf("Execute returned go err (shouldnt): %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	if !strings.Contains(res.Content, "dispatch boom") {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestAgentTool_Execute_SyncFailureFromTerminal(t *testing.T) {
	sched := &mockScheduler{
		result: scheduler.Result{
			AgentID: "a-3",
			Outcome: scheduler.SyncOutcome{
				Terminal: types.Terminal{Reason: types.TerminalMaxTurns, Message: "ran out of turns"},
			},
		},
	}
	tool := New(sched, definition.NewRegistry(), zap.NewNop())
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"x","subagent_type":"freelancer"}`))
	if !res.IsError {
		t.Fatal("expected IsError=true on non-completed Terminal")
	}
	if !strings.Contains(res.Content, "max_turns") {
		t.Errorf("Content should mention terminal reason: %q", res.Content)
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
			content: "邮件写好了，AI赋能职场.txt 已落地。",
			taskID:  "t-ae15b37c-7d6",
			want:    "邮件写好了，AI赋能职场.txt 已落地。\n\n<dispatch-meta>\ntask_id: t-ae15b37c-7d6\n</dispatch-meta>",
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
			name:    "content already ends with newline — no double blank insertion",
			content: "done\n",
			taskID:  "t-1",
			want:    "done\n\n<dispatch-meta>\ntask_id: t-1\n</dispatch-meta>",
		},
		{
			name:    "no taskID — no-op (defensive; should not happen)",
			content: "done",
			taskID:  "",
			want:    "done",
		},
		{
			name:    "duplicate basenames are deduped",
			content: "x",
			taskID:  "t-1",
			deliverables: []types.Deliverable{
				{FilePath: "/a/report.md"},
				{FilePath: "/b/report.md"},
			},
			want: "x\n\n<dispatch-meta>\ntask_id: t-1\noutputs:\n  - report.md\n</dispatch-meta>",
		},
		{
			name:    "empty FilePath is skipped",
			content: "x",
			taskID:  "t-1",
			deliverables: []types.Deliverable{
				{FilePath: ""},
				{FilePath: "/a/keep.md"},
			},
			want: "x\n\n<dispatch-meta>\ntask_id: t-1\noutputs:\n  - keep.md\n</dispatch-meta>",
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

func TestDeliverableBasenames_BackslashSeparator(t *testing.T) {
	// 防御 cross-platform — Windows 风格路径也应当被识别。
	got := deliverableBasenames([]types.Deliverable{
		{FilePath: `C:\workspace\tasks\t-1\sub\file.txt`},
	})
	if len(got) != 1 || got[0] != "file.txt" {
		t.Errorf("backslash path → basename: got %v", got)
	}
}

func TestResolveAgentType(t *testing.T) {
	cases := []struct {
		in   string
		want tool.AgentType
	}{
		// fallback / unknown
		{"", tool.AgentTypeSync},
		{"unknown-agent", tool.AgentTypeSync},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := resolveAgentType(c.in); got != c.want {
				t.Errorf("resolveAgentType(%q) = %v want %v", c.in, got, c.want)
			}
		})
	}
}
