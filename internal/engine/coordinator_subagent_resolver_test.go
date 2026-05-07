package engine

import (
	"context"
	"strings"
	"testing"
)

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

func TestLLMSubagentResolver_FallsThroughWithStubMarker(t *testing.T) {
	r := NewLLMSubagentResolver(nil)
	got, reason, err := r.Resolve(context.Background(), "调研 X", []string{"researcher", "writer"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "researcher" {
		t.Errorf("LLM stub should match heuristic for 调研; got %q", got)
	}
	if !strings.HasPrefix(reason, "(LLM stub)") {
		t.Errorf("LLM stub should prefix reason; got %q", reason)
	}
}
