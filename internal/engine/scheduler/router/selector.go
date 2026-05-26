package router

import (
	"context"
	"strings"
	"time"

	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/provider"
	pkgtypes "harnessclaw-go/pkg/types"
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

	// Long task heuristic — fire when goal exceeds 200 runes. Tasks that
	// are this long tend to be multi-deliverable rather than single-step.
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

// LLMKindSelector classifies tasks via a lightweight LLM call instead of
// keyword heuristics. Uses MaxTokens=10 so the call is cheap (~1 API round
// trip). Falls back to HeuristicKindSelector on any error so routing is
// always deterministic even when the provider is unavailable.
type LLMKindSelector struct {
	p        provider.Provider
	fallback *HeuristicKindSelector
}

// NewLLMKindSelector creates an LLMKindSelector backed by p.
func NewLLMKindSelector(p provider.Provider) *LLMKindSelector {
	return &LLMKindSelector{p: p, fallback: NewHeuristicKindSelector()}
}

const llmSelectorSystem = `You are a task classifier. Given a task description, decide the execution mode:

- "react": ONE agent can complete the entire task end-to-end, even if the task has detailed requirements
           or produces multiple related files (e.g. a document + a usage guide = still "react").
- "plan":  the task genuinely requires SEPARATE INDEPENDENT subtasks that must be executed and then
           synthesised — e.g. research multiple independent sources then write a report, build several
           unrelated components that are later integrated, or a workflow where step B cannot start until
           step A's unknown output is known.

Key rule: judge by the complexity of the GOAL (does it need truly independent parallel/sequential agents?)
NOT by the length of the requirements list or the number of output files.
When in doubt, choose "react".

Reply with exactly ONE word: react or plan. No explanation, no punctuation.`

// Select implements KindSelector using an LLM call with a 15-second timeout.
// Falls back to HeuristicKindSelector on timeout or any provider error.
func (s *LLMKindSelector) Select(goal string) types.Kind {
	if goal == "" {
		return types.KindReact
	}
	classifyGoal := goal

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := s.p.Chat(ctx, &provider.ChatRequest{
		System: llmSelectorSystem,
		Messages: []pkgtypes.Message{
			{
				Role:    pkgtypes.RoleUser,
				Content: []pkgtypes.ContentBlock{{Type: pkgtypes.ContentTypeText, Text: classifyGoal}},
			},
		},
		MaxTokens: 10,
	})
	if err != nil {
		return s.fallback.Select(goal)
	}

	var sb strings.Builder
loop:
	for {
		select {
		case <-ctx.Done():
			return s.fallback.Select(goal)
		case ev, ok := <-stream.Events:
			if !ok {
				break loop
			}
			if ev.Type == pkgtypes.StreamEventText {
				sb.WriteString(ev.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return s.fallback.Select(goal)
	}

	word := strings.ToLower(strings.TrimSpace(sb.String()))
	if strings.Contains(word, "plan") {
		return types.KindPlan
	}
	return types.KindReact
}
