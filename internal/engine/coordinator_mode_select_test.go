package engine

import (
	"context"
	"strings"
	"testing"
)

func TestHeuristicModeSelector_DefaultsToReAct(t *testing.T) {
	s := NewHeuristicModeSelector()
	cases := []string{
		"翻译这段英文",
		"写一封邮件确认会议",
		"调研下 vLLM 最新版本",
		"推荐 5 款降噪耳机",
	}
	for _, goal := range cases {
		out := s.Select(context.Background(), ModeSelectorInput{Goal: goal})
		if out.Mode != CoordinatorModeReAct {
			t.Errorf("simple task %q should route to ReAct; got %q (reason: %s)",
				goal, out.Mode, out.Reason)
		}
	}
}

func TestHeuristicModeSelector_PicksPlanForMultiDeliverable(t *testing.T) {
	s := NewHeuristicModeSelector()
	cases := []string{
		"调研大模型推理优化的最新进展，写一篇 3 章节的报告",
		"research vLLM and SGLang then write a comparison report",
		"调研三家电动车续航数据，对比一下",
	}
	for _, goal := range cases {
		out := s.Select(context.Background(), ModeSelectorInput{Goal: goal})
		if out.Mode != CoordinatorModePlan {
			t.Errorf("multi-deliverable task %q should route to Plan; got %q (reason: %s)",
				goal, out.Mode, out.Reason)
		}
	}
}

func TestHeuristicModeSelector_PicksPlanForExplicitStepSignal(t *testing.T) {
	s := NewHeuristicModeSelector()
	cases := []string{
		"分三步搞定这件事",
		"step by step solve this",
		"依次完成下面几个任务",
	}
	for _, goal := range cases {
		out := s.Select(context.Background(), ModeSelectorInput{Goal: goal})
		if out.Mode != CoordinatorModePlan {
			t.Errorf("explicit step signal in %q should route to Plan; got %q (reason: %s)",
				goal, out.Mode, out.Reason)
		}
	}
}

func TestHeuristicModeSelector_PicksPlanForLongGoal(t *testing.T) {
	s := NewHeuristicModeSelector()
	long := strings.Repeat("详细说明 ", 60) // 比 200 runes 长
	out := s.Select(context.Background(), ModeSelectorInput{Goal: long})
	if out.Mode != CoordinatorModePlan {
		t.Errorf("long task should route to Plan; got %q", out.Mode)
	}
}

func TestHeuristicModeSelector_OperatorOverrideWins(t *testing.T) {
	s := NewHeuristicModeSelector()
	// Goal looks simple but operator forced Plan.
	out := s.Select(context.Background(), ModeSelectorInput{
		Goal:         "翻译一句话",
		ExplicitMode: CoordinatorModePlan,
	})
	if out.Mode != CoordinatorModePlan {
		t.Errorf("explicit override ignored; got %q", out.Mode)
	}
	if !strings.Contains(out.Reason, "override") {
		t.Errorf("override reason should mention override; got %q", out.Reason)
	}
}

func TestHeuristicModeSelector_UnknownExplicitDoesNotCrash(t *testing.T) {
	s := NewHeuristicModeSelector()
	out := s.Select(context.Background(), ModeSelectorInput{
		Goal:         "翻译一句话",
		ExplicitMode: CoordinatorMode("garbage"),
	})
	if !out.Mode.IsKnown() {
		t.Errorf("selector returned unknown mode %q; must always return known", out.Mode)
	}
}

func TestHeuristicModeSelector_EmptyGoalReturnsReAct(t *testing.T) {
	s := NewHeuristicModeSelector()
	out := s.Select(context.Background(), ModeSelectorInput{})
	if out.Mode != CoordinatorModeReAct {
		t.Errorf("empty goal must default to ReAct; got %q", out.Mode)
	}
}

func TestLLMModeSelector_FallsThroughToHeuristic(t *testing.T) {
	s := NewLLMModeSelector(nil) // nil → uses heuristic
	out := s.Select(context.Background(), ModeSelectorInput{
		Goal: "调研 X 写 Y",
	})
	if out.Mode != CoordinatorModePlan {
		t.Errorf("LLM stub should match heuristic for multi-deliverable; got %q", out.Mode)
	}
	if !strings.HasPrefix(out.Reason, "(LLM stub)") {
		t.Errorf("stub should prefix reason with marker; got %q", out.Reason)
	}
}
