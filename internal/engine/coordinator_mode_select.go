package engine

import (
	"context"
	"strings"
)

// ModeSelector decides which CoordinatorMode should run a given task.
// Realises the "B" half of the B+D mode-selection scheme — a structured
// decision at task entry, complemented by D-mode auto-escalation if the
// chosen mode (typically ReAct) fails downstream.
//
// Implementations are interchangeable via SharedDeps.ModeSelector. We
// ship two:
//   - HeuristicModeSelector: zero-LLM default. Picks Plan when the task
//     description matches "complex" patterns, ReAct otherwise.
//   - LLMModeSelector: stub that wires to a structured-output LLM call.
//     Production rollout requires committing to a stable prompt; the
//     heuristic selector covers the common case in the meantime.
type ModeSelector interface {
	// Select returns the recommended CoordinatorMode plus a short
	// rationale string (logged at info level). Implementations must
	// never return an unknown mode — fall back to ReAct on uncertainty.
	Select(ctx context.Context, in ModeSelectorInput) ModeSelectorOutput
}

// ModeSelectorInput bundles the signals the selector consults. Pass via
// struct so adding fields (history length, prior session mode) is
// non-breaking.
type ModeSelectorInput struct {
	// Goal is the natural-language task from emma. Required.
	Goal string

	// Description is the optional 3-5 word observability label.
	Description string

	// ExplicitMode is the operator-supplied override from
	// SpawnConfig.CoordinatorMode. When non-empty the selector should
	// echo it back (operator wins). Empty means "auto-decide".
	ExplicitMode CoordinatorMode

	// AvailableSkills lets the selector reason about whether the task
	// even needs Plan mode (only one skill available → ReAct is plenty).
	AvailableSkills []string
}

// ModeSelectorOutput is the structured selection. Mode is always a
// known CoordinatorMode; Reason is a short string suitable for trace
// logs and emma's status updates.
type ModeSelectorOutput struct {
	Mode   CoordinatorMode
	Reason string
}

// HeuristicModeSelector picks Plan mode when the task looks multi-step,
// ReAct otherwise. Decision rules are deliberately conservative — the
// default should be ReAct (cheaper) unless we have positive evidence
// for Plan.
//
// Heuristics applied (any one triggers Plan):
//   - explicit operator override
//   - task contains both a research-style verb AND a write-style verb
//   - task contains explicit step count signals (例如 "三步" / "依次")
//   - task is long (>200 runes) — long tasks tend to be multi-deliverable
//
// All other tasks route to ReAct.
type HeuristicModeSelector struct{}

// NewHeuristicModeSelector returns the default selector.
func NewHeuristicModeSelector() *HeuristicModeSelector { return &HeuristicModeSelector{} }

// Select implements ModeSelector. Pure function of the input — no LLM,
// no I/O, deterministic. Lends itself to thorough unit tests, which is
// exactly the property we want for a routing decision that gates token
// spend.
func (s *HeuristicModeSelector) Select(_ context.Context, in ModeSelectorInput) ModeSelectorOutput {
	// Operator override wins; we trust the API caller knows what they
	// want. We still validate by IsKnown; an unknown explicit value
	// degrades to "auto-decide".
	if in.ExplicitMode.IsKnown() {
		return ModeSelectorOutput{
			Mode:   in.ExplicitMode,
			Reason: "explicit operator override",
		}
	}

	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return ModeSelectorOutput{
			Mode:   CoordinatorModeReAct,
			Reason: "empty goal — defaulting to react",
		}
	}

	lower := strings.ToLower(goal)

	// Explicit step-count signal — "三步"/"依次"/"分阶段" all imply
	// the user expects multi-step decomposition.
	if containsAny(lower, "三步", "四步", "五步", "依次", "分阶段", "step by step", "multi-step", "stepwise") {
		return ModeSelectorOutput{
			Mode:   CoordinatorModePlan,
			Reason: "explicit multi-step signal in goal",
		}
	}

	// research+write or research+analyze → multi-deliverable → Plan
	hasResearch := containsAny(lower, "research", "investigate", "调研", "搜集", "搜索")
	hasWrite := containsAny(lower, "write", "draft", "report", "写", "撰写", "起草", "报告")
	hasAnalyze := containsAny(lower, "analyze", "compare", "对比", "分析", "解读")
	if hasResearch && (hasWrite || hasAnalyze) {
		return ModeSelectorOutput{
			Mode:   CoordinatorModePlan,
			Reason: "multi-deliverable pattern (research+write/analyze)",
		}
	}

	// Long task heuristic — >200 runes usually means the user wrote out
	// constraints / deliverable structure / format requirements, all
	// signs of a Plan-worthy job.
	if runeCount(goal) > 200 {
		return ModeSelectorOutput{
			Mode:   CoordinatorModePlan,
			Reason: "long task description suggests multi-step work",
		}
	}

	return ModeSelectorOutput{
		Mode:   CoordinatorModeReAct,
		Reason: "default to react for short / single-deliverable tasks",
	}
}

// LLMModeSelector is a stub that documents the call site for an
// LLM-driven selector. The Select method falls through to the heuristic
// selector until a real implementation lands — that way the routing
// pipeline is testable end-to-end with the heuristic, and the LLM
// version can be slotted in without re-plumbing the mode-selection
// pathway.
//
// Why it lives in tree as a stub rather than in a separate package:
// keeping it next to the heuristic implementation makes the "default vs
// LLM" trade-off visible to anyone reviewing the file.
type LLMModeSelector struct {
	fallback ModeSelector
}

// NewLLMModeSelector wraps a heuristic-style fallback. Replace the body
// of Select with a real LLM call when the prompt format is settled.
func NewLLMModeSelector(fallback ModeSelector) *LLMModeSelector {
	if fallback == nil {
		fallback = NewHeuristicModeSelector()
	}
	return &LLMModeSelector{fallback: fallback}
}

// Select currently delegates to the fallback — the LLM call site is
// reserved for a future change that introduces the structured-decision
// prompt.
func (s *LLMModeSelector) Select(ctx context.Context, in ModeSelectorInput) ModeSelectorOutput {
	out := s.fallback.Select(ctx, in)
	out.Reason = "(LLM stub) " + out.Reason
	return out
}

// runeCount returns the rune count of s without dragging in a heavy
// utf8 dependency for one call site.
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
