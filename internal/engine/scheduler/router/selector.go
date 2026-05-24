package router

import (
	"strings"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// KindSelector decides between KindReact and KindPlan based on goal text.
// Mirrors the heuristics from internal/engine/coordinator_mode_select.go.
type KindSelector interface {
	Select(goal string) types.Kind
}

// HeuristicKindSelector picks Plan mode when the task looks multi-step,
// ReAct otherwise. Decision rules are deliberately conservative — the
// default should be ReAct (cheaper) unless we have positive evidence
// for Plan.
//
// Heuristics applied (any one triggers Plan):
//   - task contains both a research-style verb AND a write-style verb
//   - task contains explicit step count signals (例如 "三步" / "依次")
//   - task is long (>200 runes) — long tasks tend to be multi-deliverable
//
// All other tasks route to ReAct.
type HeuristicKindSelector struct{}

// NewHeuristicKindSelector returns the default selector.
func NewHeuristicKindSelector() *HeuristicKindSelector { return &HeuristicKindSelector{} }

// Select implements KindSelector. Pure function of the input — no LLM,
// no I/O, deterministic. Lends itself to thorough unit tests, which is
// exactly the property we want for a routing decision that gates token
// spend.
func (s *HeuristicKindSelector) Select(goal string) types.Kind {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return types.KindReact
	}

	lower := strings.ToLower(goal)

	// Explicit step-count signal — "三步"/"依次"/"分阶段" all imply
	// the user expects multi-step decomposition.
	if containsAny(lower, "三步", "四步", "五步", "依次", "分阶段", "step by step", "multi-step", "stepwise") {
		return types.KindPlan
	}

	// research+write or research+analyze → multi-deliverable → Plan
	hasResearch := containsAny(lower, "research", "investigate", "调研", "搜集", "搜索")
	hasWrite := containsAny(lower, "write", "draft", "report", "写", "撰写", "起草", "报告")
	hasAnalyze := containsAny(lower, "analyze", "compare", "对比", "分析", "解读")
	if hasResearch && (hasWrite || hasAnalyze) {
		return types.KindPlan
	}

	// Long task heuristic — >200 runes usually means the user wrote out
	// constraints / deliverable structure / format requirements, all
	// signs of a Plan-worthy job.
	if runeLen(goal) > 200 {
		return types.KindPlan
	}

	return types.KindReact
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// runeLen returns the rune count of s without dragging in a heavy
// utf8 dependency for one call site.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
