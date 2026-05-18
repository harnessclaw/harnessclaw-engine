package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// fakeResolverProvider drives LLMSubagentResolver in tests: returns
// either a pre-canned tool call (toolInput populated), text-only
// response (textOnly), or an immediate error (chatErr).
type fakeResolverProvider struct {
	toolInput string
	textOnly  string
	chatErr   error
}

func (p *fakeResolverProvider) Name() string { return "fake-resolver-provider" }

func (p *fakeResolverProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func (p *fakeResolverProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	if p.chatErr != nil {
		return nil, p.chatErr
	}
	ch := make(chan types.StreamEvent, 4)
	go func() {
		defer close(ch)
		switch {
		case p.toolInput != "":
			ch <- types.StreamEvent{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{
					ID:    "tu_resolver",
					Name:  llmResolverToolName,
					Input: p.toolInput,
				},
			}
		case p.textOnly != "":
			ch <- types.StreamEvent{Type: types.StreamEventText, Text: p.textOnly}
		}
		ch <- types.StreamEvent{Type: types.StreamEventMessageEnd, StopReason: "end_turn"}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func TestHeuristicSubagentResolver_KeywordRouting(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	available := []string{"researcher", "writer", "analyst", "developer",
		"travel_planner", "recommender", "scheduler", "general-purpose"}

	cases := []struct {
		goal      string
		want      string
	}{
		{"翻译这段英文", "writer"},
		{"写一封正式邮件", "writer"},
		{"调研 vLLM 最新版本", "researcher"},
		{"分析这份 Q4 销售数据", "analyst"},
		{"写一个 Go HTTP 中间件", "developer"},
		{"规划周末北京 2 天行程", "travel_planner"},
		{"推荐降噪耳机 5 款", "recommender"},
		{"帮我排下周日程", "scheduler"},
	}
	for _, c := range cases {
		t.Run(c.goal, func(t *testing.T) {
			got, reason, err := r.Resolve(context.Background(), c.goal, available)
			if err != nil {
				t.Fatalf("resolve error: %v", err)
			}
			if got != c.want {
				t.Errorf("goal %q: want %q, got %q (reason=%s)", c.goal, c.want, got, reason)
			}
		})
	}
}

// TestHeuristicSubagentResolver_VerbBeatsObjectOnTie pins the bug fix:
// when a goal matches both a verb-based role (researcher / analyst) AND
// developer's object keywords (代码 / 脚本 / 中间件), the verb role must
// win. The previous resolver tried developer first and returned it
// blindly, mis-routing every "调研 X 代码" / "分析 Y 脚本" task.
func TestHeuristicSubagentResolver_VerbBeatsObjectOnTie(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	available := []string{"researcher", "writer", "analyst", "developer", "general-purpose"}

	cases := []struct {
		goal string
		want string
	}{
		{"调研 OpenClaw 代码架构", "researcher"},        // 调研 + 代码 tie → researcher
		{"调研 Python 生态现状", "researcher"},          // 调研 + python tie → researcher
		{"对比两个脚本方案的优劣", "analyst"},             // 对比 + 脚本 tie → analyst
		{"分析中间件性能数据", "analyst"},                 // 分析 + 中间件 tie → analyst
		{"撰写代码评审报告", "writer"},                   // 撰写 + 代码 tie → writer
		// English-language tasks where the old resolver tripped on
		// substring noise (code in codex, go in "go from", etc.).
		{"research how to go from prototype to production", "researcher"},
		{"research the Codex model architecture", "researcher"},
		{"analyze function approximation tradeoffs", "analyst"},
	}
	for _, c := range cases {
		t.Run(c.goal, func(t *testing.T) {
			got, reason, err := r.Resolve(context.Background(), c.goal, available)
			if err != nil {
				t.Fatalf("resolve error: %v", err)
			}
			if got != c.want {
				t.Errorf("goal %q: want %q, got %q (reason=%s)", c.goal, c.want, got, reason)
			}
		})
	}
}

// TestHeuristicSubagentResolver_PureDeveloperTaskStillRoutesCorrectly
// guards against over-correction: tasks that are unambiguously
// developer (no research / analysis verb in sight) must still land at
// developer.
func TestHeuristicSubagentResolver_PureDeveloperTaskStillRoutesCorrectly(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	available := []string{"researcher", "writer", "analyst", "developer", "general-purpose"}

	cases := []struct {
		goal string
		want string
	}{
		{"实现一个 Python 中间件", "developer"},
		{"调试 typescript 编译错误", "developer"},
		{"compile the rust backend", "developer"}, // English compile keyword
	}
	for _, c := range cases {
		t.Run(c.goal, func(t *testing.T) {
			got, _, err := r.Resolve(context.Background(), c.goal, available)
			if err != nil {
				t.Fatalf("resolve error: %v", err)
			}
			if got != c.want {
				t.Errorf("goal %q: want %q, got %q", c.goal, c.want, got)
			}
		})
	}
}

func TestHeuristicSubagentResolver_FallsBackToGeneralPurpose(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	got, reason, err := r.Resolve(context.Background(),
		"do something I haven't taught the matchers",
		[]string{"general-purpose", "writer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "general-purpose" {
		t.Errorf("expected general-purpose; got %q (%s)", got, reason)
	}
}

func TestHeuristicSubagentResolver_PicksFirstAvailableWhenGeneralAbsent(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	got, _, err := r.Resolve(context.Background(),
		"really weird unmatchable thing",
		[]string{"researcher", "writer"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("first available should win; got %q", got)
	}
}

func TestHeuristicSubagentResolver_RejectsEmptyAvailable(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	if _, _, err := r.Resolve(context.Background(), "x", nil); err == nil {
		t.Error("empty available list should error")
	}
}

func TestHeuristicSubagentResolver_HandlesEmptyGoal(t *testing.T) {
	r := NewHeuristicSubagentResolver()
	got, _, err := r.Resolve(context.Background(), "",
		[]string{"writer", "general-purpose"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "general-purpose" {
		t.Errorf("empty goal should fall back to general-purpose; got %q", got)
	}
}

// LLMSubagentResolver tests start here. The resolver delegates to a
// fallback when nil provider — exercise that path explicitly because
// production deployments without LLM credentials still need a working
// resolver.
func TestLLMSubagentResolver_NilProviderUsesFallback(t *testing.T) {
	r := NewLLMSubagentResolver(nil, "", nil, nil, nil)
	got, reason, err := r.Resolve(context.Background(), "调研 X",
		[]string{"researcher", "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("nil provider should fall back to heuristic; got %q", got)
	}
	if !strings.Contains(reason, "no LLM provider") {
		t.Errorf("reason should explain why fallback fired; got %q", reason)
	}
}

// Single-candidate short-circuit: don't burn an LLM call when there's
// only one valid pick.
func TestLLMSubagentResolver_SingleCandidateShortCircuits(t *testing.T) {
	r := NewLLMSubagentResolver(&fakeResolverProvider{}, "test-model", nil, nil, nil)
	got, _, err := r.Resolve(context.Background(), "anything", []string{"only-one"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "only-one" {
		t.Errorf("single available should return it directly; got %q", got)
	}
}

// Happy path: model picks a valid candidate and the resolver returns
// it with the "(LLM)" reason prefix.
func TestLLMSubagentResolver_ModelPickValidCandidate(t *testing.T) {
	prov := &fakeResolverProvider{
		toolInput: `{"subagent_type":"researcher","rationale":"调研类任务"}`,
	}
	r := NewLLMSubagentResolver(prov, "test-model", nil, nil, nil)
	got, reason, err := r.Resolve(context.Background(),
		"调研 OpenClaw 代码架构",
		[]string{"researcher", "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("model picked researcher; resolver returned %q", got)
	}
	if !strings.HasPrefix(reason, "(LLM)") {
		t.Errorf("LLM-driven reason should be prefixed; got %q", reason)
	}
}

// Out-of-set protection: if the model picks something not in
// available, the resolver must NOT trust it — falls back to heuristic.
func TestLLMSubagentResolver_RejectsOutOfSetPick(t *testing.T) {
	prov := &fakeResolverProvider{
		toolInput: `{"subagent_type":"phantom_role","rationale":"made it up"}`,
	}
	r := NewLLMSubagentResolver(prov, "test-model", nil, nil, nil)
	got, reason, err := r.Resolve(context.Background(),
		"调研 X",
		[]string{"researcher", "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("invalid LLM pick should fall back to heuristic researcher; got %q", got)
	}
	if !strings.Contains(reason, "phantom_role") {
		t.Errorf("reason should explain LLM picked an invalid name; got %q", reason)
	}
}

// LLM call error: provider.Chat fails. Resolver must catch and fall
// back rather than propagate the error to the scheduler.
func TestLLMSubagentResolver_HandlesLLMError(t *testing.T) {
	prov := &fakeResolverProvider{chatErr: errors.New("network down")}
	r := NewLLMSubagentResolver(prov, "test-model", nil, nil, nil)
	got, reason, err := r.Resolve(context.Background(),
		"分析 X",
		[]string{"researcher", "analyst"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "analyst" {
		t.Errorf("on LLM error should fall back; expected analyst, got %q", got)
	}
	if !strings.Contains(reason, "LLM error") {
		t.Errorf("reason should explain the failure; got %q", reason)
	}
}

// Model returns a stream with no tool call (model just emits text).
// Resolver treats this as a failure → fallback.
func TestLLMSubagentResolver_HandlesNoToolCall(t *testing.T) {
	prov := &fakeResolverProvider{textOnly: "I think researcher is best"}
	r := NewLLMSubagentResolver(prov, "test-model", nil, nil, nil)
	got, reason, err := r.Resolve(context.Background(),
		"调研 X",
		[]string{"researcher", "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("no-tool-call should fall back; got %q", got)
	}
	if !strings.Contains(reason, "did not call") {
		t.Errorf("reason should explain missing tool call; got %q", reason)
	}
}

func TestLLMSubagentResolver_FreelancerInEnum(t *testing.T) {
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	listings := reg.ListForPlanner()
	found := false
	for _, l := range listings {
		if l.Name == "freelancer" {
			found = true
			break
		}
	}
	if !found {
		t.Error("freelancer should appear in ListForPlanner so the resolver enum sees it")
	}
}
