package engine

import (
	"context"
	"fmt"
	"strings"
)

// PlannerInput carries everything a Planner needs to produce a Plan. We
// pass a struct rather than positional args so adding fields (e.g. budget
// hints, prior results from a re-plan) doesn't break implementations.
type PlannerInput struct {
	// Goal is the natural-language task description from emma.
	Goal string

	// Description is the optional 3-5 word observability label.
	Description string

	// AvailableSubagents lists the L3 sub-agent names the Planner /
	// SubagentResolver may reference. Bound by the runtime
	// AgentDefinitionRegistry — passing the list in (rather than letting
	// the Planner introspect a global) keeps the Planner pure and
	// testable.
	//
	// Renamed from AvailableSkills (v1.16) to disambiguate from
	// AgentDefinition.Skills (which is the L3's capability tag list,
	// a different concept).
	AvailableSubagents []string

	// Escalation is non-nil when the Planner is being asked to re-plan
	// after a ReAct failure or a previous Plan run miss. The Planner
	// should preserve already-produced artifacts (avoid duplicating
	// completed work) and only schedule the missing pieces.
	Escalation *EscalationContext
}

// PlannerOutput wraps the Plan plus a machine-readable rationale string.
// Rationale isn't load-bearing for execution — it's surfaced to logs and
// trace events so operators can debug "why did the Planner choose 1 step
// when I expected 3".
type PlannerOutput struct {
	Plan      *Plan
	Rationale string
}

// Planner produces a Plan from a PlannerInput. Implementations:
//   - HeuristicPlanner — code-only, no LLM (default; used when the LLM
//     planner is unavailable or for unit tests)
//   - llmPlanner       — calls Sonnet to decompose the task (registered
//     post-construction by callers that want LLM-driven planning)
//
// The interface is deliberately small so adding a third strategy
// (e.g. a "skill-affinity-only" deterministic planner for benchmarking)
// requires implementing one method.
type Planner interface {
	Plan(ctx context.Context, in PlannerInput) (*PlannerOutput, error)
}

// HeuristicPlanner is the deterministic, no-LLM Planner. It produces a
// minimal step DAG without binding executors — PlanStep.SubagentType is
// left empty by design; the Scheduler resolves the executor at dispatch
// time via SubagentResolver.
//
// Why we ship this even after an LLM Planner exists: it's the safe
// default for tests, for offline mode, and for budget-blown re-plans
// where another LLM call is the last thing we want.
type HeuristicPlanner struct{}

// NewHeuristicPlanner returns the default heuristic planner.
func NewHeuristicPlanner() *HeuristicPlanner { return &HeuristicPlanner{} }

// Plan decomposes the goal using a few hand-rolled rules. Each rule
// corresponds to a phrasing pattern that appears repeatedly in real
// dispatches; when none match, we emit a single-step plan so the
// Scheduler still has something to chew on.
//
// The matchers are case-insensitive and operate on simple substring
// presence — that's deliberate; brittle exact-phrase matching would just
// push the burden onto users to phrase requests "correctly".
//
// Note (v1.16): the rules previously also pre-bound each step to a
// specific L3 sub-agent (PlanStep.Skill). That binding has moved to
// SubagentResolver — Planner now only decomposes "what to do", not
// "who does it".
func (p *HeuristicPlanner) Plan(_ context.Context, in PlannerInput) (*PlannerOutput, error) {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return nil, fmt.Errorf("planner: empty goal")
	}
	if len(in.AvailableSubagents) == 0 {
		return nil, fmt.Errorf("planner: no available sub-agents to dispatch")
	}

	lower := strings.ToLower(goal)

	// Rule 1: "research X then write Y" / "调研 X 写 Y" → 2 steps
	hasResearchVerb := containsAny(lower, "research", "investigate", "调研", "搜集", "搜索", "查一下", "查下")
	hasWriteVerb := containsAny(lower, "write", "draft", "report", "写", "撰写", "起草", "报告")
	if hasResearchVerb && hasWriteVerb {
		return &PlannerOutput{
			Plan: &Plan{
				Goal: goal,
				Steps: []*PlanStep{
					{ID: "s1", Description: "调研 / 收集背景信息", Prompt: goal},
					{ID: "s2", Description: "基于调研撰写产物",
						Prompt: goal, DependsOn: []string{"s1"}},
				},
			},
			Rationale: "research+write pattern detected",
		}, nil
	}

	// Rule 2: "research+analyze" → 2 steps
	hasAnalyzeVerb := containsAny(lower, "analyze", "compare", "对比", "分析", "解读")
	if hasResearchVerb && hasAnalyzeVerb {
		return &PlannerOutput{
			Plan: &Plan{
				Goal: goal,
				Steps: []*PlanStep{
					{ID: "s1", Description: "调研 / 收集数据", Prompt: goal},
					{ID: "s2", Description: "分析数据得出结论",
						Prompt: goal, DependsOn: []string{"s1"}},
				},
			},
			Rationale: "research+analyze pattern detected",
		}, nil
	}

	// Rule 3: fall through to a single step. Executor will be picked by
	// SubagentResolver at dispatch time.
	return &PlannerOutput{
		Plan: &Plan{
			Goal: goal,
			Steps: []*PlanStep{
				{ID: "s1", Description: "single-step execution", Prompt: goal},
			},
		},
		Rationale: "no specific pattern matched; defaulting to single step",
	}, nil
}

// matchesSubagent maps a natural-language goal to a likely L3 sub-agent
// type. Used by the SubagentResolver (and previously by HeuristicPlanner
// rule 3 before the executor decision moved to dispatch time).
func matchesSubagent(goal, subagent string) bool {
	switch subagent {
	case "writer":
		return containsAny(goal, "write", "draft", "polish", "translate", "邮件", "翻译", "润色", "撰写")
	case "researcher":
		return containsAny(goal, "research", "find out", "调研", "查一下", "搜集")
	case "analyst":
		return containsAny(goal, "analyze", "compare", "对比", "分析")
	case "developer":
		return containsAny(goal, "code", "function", "script", "debug",
			"middleware", "中间件", "代码", "脚本", "调试", "go ", "python", "typescript")
	case "travel_planner":
		return containsAny(goal, "travel", "trip", "itinerary", "出行", "行程")
	case "recommender":
		return containsAny(goal, "recommend", "best", "pick", "推荐", "选购", "比价")
	case "scheduler":
		return containsAny(goal, "schedule", "calendar", "日程", "排期", "会议")
	}
	return false
}

// subagentSet is a tiny set helper used by the planner / resolver.
type subagentSet map[string]struct{}

func newSubagentSet(items []string) subagentSet {
	out := make(subagentSet, len(items))
	for _, s := range items {
		out[s] = struct{}{}
	}
	return out
}

func (s subagentSet) has(name string) bool { _, ok := s[name]; return ok }

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
