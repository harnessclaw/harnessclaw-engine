package engine

import (
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/pkg/types"
)

// FallbackChain produces a graceful degraded response when a coordinator
// can't deliver a fully-passing run. Triggered by:
//   - BudgetTracker.Exceeded() returning true mid-run
//   - Judge.ReviewGoal failing on the final attempt (no more re-plans)
//   - Plan/ReAct internal error before we could re-plan
//
// The chain's job is NOT to retry — that's the coordinator's domain.
// FallbackChain just aggregates whatever has been produced so far into a
// shape the parent (emma) can present honestly to the user.
type FallbackChain struct {
	logger *zap.Logger
}

// NewFallbackChain constructs the singleton-per-task fallback aggregator.
func NewFallbackChain(logger *zap.Logger) *FallbackChain {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FallbackChain{logger: logger.Named("fallback")}
}

// FallbackInput bundles everything the chain needs. Pass via struct so
// adding fields is non-breaking.
type FallbackInput struct {
	// Goal echoes the task description so the summary can reference it.
	Goal string

	// Reason describes why fallback was triggered (budget / judge fail /
	// internal error). Surfaced in the summary's first line.
	Reason string

	// Results is the list of step results gathered so far. Failed steps
	// are included — the summary distinguishes them from successes.
	Results []*StepResult

	// Budget is the consumption snapshot at fallback time. Empty when
	// the trigger is judge-fail (still inside budget).
	Budget BudgetSnapshot
}

// FallbackOutput is what the chain produces. Mirrors the shape
// SpawnResult / ResultEnvelope expects, so the coordinator can return it
// directly rather than rebuilding.
type FallbackOutput struct {
	// Summary is a short user-facing-ish text the parent can paraphrase.
	// It describes what was accomplished and what was missed.
	Summary string

	// Artifacts is the union of every artifact produced by any step
	// (success OR partial). The parent / user keeps access to partial
	// value rather than losing everything to a single failure.
	Artifacts []types.ArtifactRef

	// NeedsAttention is the structured "what's still missing" list,
	// suitable for emma to relay to the user verbatim.
	NeedsAttention []string

	// Reason is the original Reason from the input, propagated for log.
	Reason string
}

// Aggregate runs the chain. The implementation is deterministic — the
// same input always produces the same output, which makes it trivially
// testable and makes Fallback summaries reproducible across runs.
func (f *FallbackChain) Aggregate(in FallbackInput) *FallbackOutput {
	f.logger.Info("fallback triggered",
		zap.String("reason", in.Reason),
		zap.Int("step_results", len(in.Results)),
		zap.Bool("budget_exceeded", in.Budget.Exceeded),
	)

	out := &FallbackOutput{Reason: in.Reason}

	// Aggregate artifacts in step order, dedup by ID. Step order matters
	// because the parent will tend to reference earlier outputs first;
	// keeping insertion order preserves the natural narrative.
	seen := make(map[string]struct{})
	for _, r := range in.Results {
		if r == nil {
			continue
		}
		for _, a := range r.Artifacts {
			if _, dup := seen[a.ArtifactID]; dup {
				continue
			}
			seen[a.ArtifactID] = struct{}{}
			out.Artifacts = append(out.Artifacts, a)
		}
	}

	// Identify successful vs failed steps so the summary can split them.
	var ok, failed []*StepResult
	for _, r := range in.Results {
		if r == nil {
			continue
		}
		if r.Status == "success" {
			ok = append(ok, r)
		} else {
			failed = append(failed, r)
		}
	}

	// Sort failed steps by StepID for stable summary text — useful so
	// log diffs in tests don't churn.
	sort.Slice(failed, func(i, j int) bool { return failed[i].StepID < failed[j].StepID })

	// Build the summary string. Format kept deliberately simple; emma
	// can paraphrase or expand as needed.
	var b strings.Builder
	if in.Goal != "" {
		fmt.Fprintf(&b, "原任务：%s\n", in.Goal)
	}
	fmt.Fprintf(&b, "降级原因：%s\n", in.Reason)
	if len(ok) > 0 {
		fmt.Fprintf(&b, "已完成 %d 步：%s\n",
			len(ok), strings.Join(stepIDs(ok), ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "未完成 %d 步：%s\n",
			len(failed), strings.Join(stepIDs(failed), ", "))
	}
	if len(out.Artifacts) > 0 {
		fmt.Fprintf(&b, "已产出 %d 个 artifact，可供后续使用\n", len(out.Artifacts))
	}
	if in.Budget.Exceeded {
		fmt.Fprintf(&b, "(预算耗尽：%s)\n", in.Budget.ExceededWhy)
	}
	out.Summary = strings.TrimSpace(b.String())

	// NeedsAttention enumerates concrete gaps so emma can surface them
	// to the user rather than swallowing.
	for _, r := range failed {
		out.NeedsAttention = append(out.NeedsAttention,
			fmt.Sprintf("step %s 未完成 (status=%s)", r.StepID, r.Status))
	}
	if in.Budget.Exceeded {
		out.NeedsAttention = append(out.NeedsAttention, in.Budget.ExceededWhy)
	}

	return out
}

func stepIDs(rs []*StepResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.StepID
	}
	return out
}
