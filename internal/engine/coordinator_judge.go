package engine

import (
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

// JudgeVerdict is the structured outcome of any review_* call. Pass==true
// is the only success state; Reasons explains the verdict (always populated
// on fail, optional on pass).
type JudgeVerdict struct {
	Pass    bool
	Reasons []string
	// Tier records which level produced the verdict — used by telemetry to
	// answer "how often does L3 LLM Judge actually fire?". Empty when the
	// verdict is the default-pass with no checks.
	Tier string // "rule" | "schema" | "llm"
}

// String implements zap.Stringer for compact logging.
func (v JudgeVerdict) String() string {
	verdict := "pass"
	if !v.Pass {
		verdict = "fail"
	}
	return fmt.Sprintf("%s[%s] reasons=%v", verdict, v.Tier, v.Reasons)
}

// Judge runs progressive review checks: rule (free), then schema (free),
// then LLM (expensive). Each tier short-circuits — once a fail is found,
// later tiers don't run. This matches architecture_v2.md §"图 3" Judge
// 三层渐进 and minimises LLM cost.
type Judge struct {
	logger *zap.Logger
}

// NewJudge constructs the singleton-per-task Judge. logger may be nil
// (tests).
func NewJudge(logger *zap.Logger) *Judge {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Judge{logger: logger.Named("judge")}
}

// ReviewPlan validates a Plan structurally. Tier "rule" — purely
// code-based; calling Validate plus a few guardrails is enough.
func (j *Judge) ReviewPlan(p *Plan) JudgeVerdict {
	v := JudgeVerdict{Tier: "rule", Pass: true}
	if err := p.Validate(); err != nil {
		v.Pass = false
		v.Reasons = append(v.Reasons, err.Error())
		return v
	}
	if len(p.Steps) > 20 {
		// Heuristic: a plan with >20 steps almost always has been
		// over-decomposed by an LLM that lost track. Surface as a
		// warning the coordinator can use to re-plan.
		v.Pass = false
		v.Reasons = append(v.Reasons, fmt.Sprintf("plan too long: %d steps (>20 indicates over-decomposition)", len(p.Steps)))
		return v
	}
	// Empty Goal isn't fatal but we flag it.
	if p.Goal == "" {
		v.Pass = false
		v.Reasons = append(v.Reasons, "plan missing Goal")
	}
	return v
}

// ReviewStep validates a single PlanStep's result. Tier "rule" + optional
// "schema" if the step declared an OutputSchema.
//
// Rules applied at L1:
//   - status == "success"
//   - at least one artifact produced when ExpectedOutputs is non-empty
//   - every required ExpectedOutput.Role is covered by some Artifact
//
// Schema validation (L2) reuses the existing in-house validator from
// submittool — same code path SubmitTaskResult uses, so the verdict is
// consistent across L2's per-step Judge and the L3 driver's submit-time
// validation.
func (j *Judge) ReviewStep(step *PlanStep, result *StepResult) JudgeVerdict {
	v := JudgeVerdict{Tier: "rule", Pass: true}

	if result == nil {
		v.Pass = false
		v.Reasons = []string{"step result is nil"}
		return v
	}
	if result.Status != "success" {
		v.Pass = false
		v.Reasons = append(v.Reasons, fmt.Sprintf("status %q != success", result.Status))
	}

	required := requiredRoles(step.ExpectedOutputs)
	if len(required) > 0 {
		if len(result.Artifacts) == 0 {
			v.Pass = false
			v.Reasons = append(v.Reasons, "step requires artifacts but none produced")
		} else {
			missing := missingRoles(required, result.Artifacts)
			if len(missing) > 0 {
				v.Pass = false
				v.Reasons = append(v.Reasons,
					fmt.Sprintf("required roles missing: %v", missing))
			}
		}
	}

	// L2 schema check is left to SubmitTaskResult itself — by the time
	// the result reaches Judge, it has already passed schema validation
	// (or failed loudly). We don't re-run schema here to avoid double
	// costs; instead, we trust ContractFailures forwarded via
	// result.Failures.
	if len(result.Failures) > 0 {
		v.Pass = false
		v.Reasons = append(v.Reasons, result.Failures...)
	}

	return v
}

// ReviewGoal is the L3 LLM-driven check: did the aggregate of all steps
// actually satisfy the user's goal? Conceptually expensive — runs once at
// the end of a Plan execution. The current implementation is a stub
// (defaults to pass when status is success); replacing it with a real
// LLM call is a follow-up that requires committing to a goal-review
// prompt format we haven't yet stabilised.
//
// Until the LLM body lands, we still do basic sanity:
//   - any step failed → fail
//   - zero artifacts produced → fail
// This catches the obvious "Plan ran but produced nothing useful" path.
func (j *Judge) ReviewGoal(goal string, results []*StepResult) JudgeVerdict {
	v := JudgeVerdict{Tier: "llm", Pass: true}

	if goal == "" {
		v.Pass = false
		v.Reasons = []string{"empty goal"}
		return v
	}
	if len(results) == 0 {
		v.Pass = false
		v.Reasons = []string{"no step results to judge"}
		return v
	}
	totalArt := 0
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Status != "success" {
			v.Pass = false
			v.Reasons = append(v.Reasons,
				fmt.Sprintf("step %s ended in status %q", r.StepID, r.Status))
		}
		totalArt += len(r.Artifacts)
	}
	if totalArt == 0 {
		v.Pass = false
		v.Reasons = append(v.Reasons, "no artifacts produced across all steps")
	}
	// TODO(plan-mode): wire this to a Sonnet-side review_goal prompt
	// once the prompt format has stabilised. Until then the verdict is
	// rule-tier in disguise — we report Tier="llm" so telemetry already
	// reflects the intended call site.
	return v
}

// requiredRoles is a small helper — slimmer than submittool's because
// the Judge only needs role names. Mirrors the agent.RenderExpectedOutputs
// logic for what counts as "required".
func requiredRoles(outs []types.ExpectedOutput) []string {
	roles := make([]string, 0, len(outs))
	for _, o := range outs {
		if !o.Required {
			continue
		}
		roles = append(roles, o.Role)
	}
	return roles
}

func missingRoles(required []string, artifacts []types.ArtifactRef) []string {
	covered := make(map[string]bool, len(artifacts))
	for _, a := range artifacts {
		if a.Role != "" {
			covered[a.Role] = true
		}
	}
	var missing []string
	for _, r := range required {
		if !covered[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// validateSubmissionResult provides a tiny adapter so callers wanting to
// drive Judge from a SubmitTaskResult outcome can do so without touching
// the schema package directly. Currently unused by the coordinator (the
// L3 driver already validates), kept so future Plan-mode hooks can call
// it before accepting a step's submission.
func validateSubmissionResult(schema, value map[string]any) []string {
	return submittool.ValidateAgainstSchema(schema, value)
}
