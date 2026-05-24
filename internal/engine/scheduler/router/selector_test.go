package router_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/router"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestHeuristicKindSelector_Defaults(t *testing.T) {
	sel := router.NewHeuristicKindSelector()
	tests := []struct {
		goal string
		want types.Kind
	}{
		{"write a hello world script", types.KindReact},
		{"step by step migrate the database", types.KindPlan},
		{"三步完成数据清洗", types.KindPlan},
		{"research cloud providers and write a comparison report", types.KindPlan},
		{"调研竞品并撰写分析报告", types.KindPlan},
		{"", types.KindReact},
	}
	for _, tc := range tests {
		got := sel.Select(tc.goal)
		if got != tc.want {
			t.Errorf("Select(%q) = %s, want %s", tc.goal, got, tc.want)
		}
	}
}

func TestHeuristicKindSelector_LongGoal(t *testing.T) {
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	sel := router.NewHeuristicKindSelector()
	if sel.Select(string(long)) != types.KindPlan {
		t.Fatal("expected Plan for long goal (>200 runes)")
	}
}
