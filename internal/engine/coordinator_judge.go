package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
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
	logger   *zap.Logger
	provider provider.Provider
	model    string
	retryer  *retry.Retryer
	timeouts llmCallTimeouts
}

// NewJudge constructs the singleton-per-task Judge. logger may be nil
// (tests). Wire an LLM via WithLLM + WithRetry before use in plan mode.
func NewJudge(logger *zap.Logger) *Judge {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Judge{logger: logger.Named("judge")}
}

// WithLLM attaches an LLM provider so ReviewGoal can call the model.
// model may be empty (provider uses its configured default). Returns j
// for chaining.
func (j *Judge) WithLLM(p provider.Provider, model string) *Judge {
	j.provider = p
	j.model = model
	return j
}

// WithRetry attaches the engine-level Retryer and call timeouts so
// ReviewGoal LLM calls inherit the same retry/heartbeat/visibility stack
// as LLMPlanner / LLMSubagentResolver.
func (j *Judge) WithRetry(retryer *retry.Retryer, timeouts llmCallTimeouts) *Judge {
	j.retryer = retryer
	j.timeouts = timeouts
	return j
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
// submittool — same code path submit_task_result uses, so the verdict is
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

	// L2 schema check is left to submit_task_result itself — by the time
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

// ReviewGoal checks whether the aggregate plan output actually satisfied
// the user's goal.
//
// Two-tier flow:
//  1. Rule (free): any step failed → instant fail, skip LLM.
//  2. LLM (when provider wired): call model with goal + step summaries;
//     structured tool output gives pass/fail + reasons.
//     On LLM error the rule-level pass-through applies so a transient
//     network hiccup never blocks a completed plan.
//
// Without an LLM provider the method degrades to pure rule-based: all
// steps success → pass, any failure → fail.
func (j *Judge) ReviewGoal(ctx context.Context, goal string, results []*StepResult) JudgeVerdict {
	// Guard: empty inputs.
	if goal == "" {
		return JudgeVerdict{Tier: "rule", Pass: false, Reasons: []string{"empty goal"}}
	}
	if len(results) == 0 {
		return JudgeVerdict{Tier: "rule", Pass: false, Reasons: []string{"no step results to judge"}}
	}

	// L1 rule tier: check step statuses — cheap, no LLM cost.
	var ruleFailures []string
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.Status != "success" {
			ruleFailures = append(ruleFailures,
				fmt.Sprintf("step %s ended in status %q", r.StepID, r.Status))
		}
	}
	if len(ruleFailures) > 0 {
		return JudgeVerdict{Tier: "rule", Pass: false, Reasons: ruleFailures}
	}

	// All steps succeeded — call LLM for semantic judgment when wired.
	if j.provider == nil {
		return JudgeVerdict{Tier: "rule", Pass: true}
	}
	return j.reviewGoalLLM(ctx, goal, results)
}

// reviewGoalLLM sends one structured-output request to the LLM and
// interprets the emit_goal_review tool call. On any failure it logs a
// warning and returns a pass verdict so a transient LLM error never
// blocks an otherwise-complete plan.
func (j *Judge) reviewGoalLLM(ctx context.Context, goal string, results []*StepResult) JudgeVerdict {
	req := &provider.ChatRequest{
		Model:  j.model,
		System: buildGoalReviewSystemPrompt(),
		Messages: []types.Message{{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: buildGoalReviewUserMessage(goal, results)}},
		}},
		Tools:     []provider.ToolSchema{goalReviewEmitTool()},
		MaxTokens: 1024,
		// Temperature 0: deterministic assessment, not creative interpretation.
		Temperature: 0,
	}

	var toolInput string
	agentID, out := retryRoutingFromCtx(ctx)
	if j.retryer != nil {
		result := callLLM(ctx, j.provider, req, j.logger, j.retryer, j.timeouts, agentID, out, out)
		if result == nil || result.streamErr != nil {
			err := result.streamErr
			if result == nil {
				err = fmt.Errorf("nil result")
			}
			j.logger.Warn("judge: ReviewGoal LLM call failed; defaulting to pass",
				zap.Error(err),
			)
			return JudgeVerdict{Tier: "llm", Pass: true, Reasons: []string{"LLM unavailable; rule-pass applied"}}
		}
		for _, tc := range result.toolCalls {
			if tc.Name == goalReviewToolName {
				toolInput = tc.Input
			}
		}
	} else {
		stream, err := j.provider.Chat(ctx, req)
		if err != nil {
			j.logger.Warn("judge: ReviewGoal LLM call failed; defaulting to pass", zap.Error(err))
			return JudgeVerdict{Tier: "llm", Pass: true, Reasons: []string{"LLM unavailable; rule-pass applied"}}
		}
		for ev := range stream.Events {
			if ev.Type == types.StreamEventToolUse && ev.ToolCall != nil &&
				ev.ToolCall.Name == goalReviewToolName {
				toolInput = ev.ToolCall.Input
			}
		}
		if err := stream.Err(); err != nil {
			j.logger.Warn("judge: ReviewGoal stream error; defaulting to pass", zap.Error(err))
			return JudgeVerdict{Tier: "llm", Pass: true, Reasons: []string{"LLM stream error; rule-pass applied"}}
		}
	}

	if toolInput == "" {
		j.logger.Warn("judge: ReviewGoal model did not call tool; defaulting to pass",
			zap.String("tool", goalReviewToolName),
		)
		return JudgeVerdict{Tier: "llm", Pass: true, Reasons: []string{"model did not emit review; rule-pass applied"}}
	}

	var parsed struct {
		Pass    bool     `json:"pass"`
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal([]byte(toolInput), &parsed); err != nil {
		j.logger.Warn("judge: ReviewGoal tool input parse failed; defaulting to pass", zap.Error(err))
		return JudgeVerdict{Tier: "llm", Pass: true, Reasons: []string{"parse error; rule-pass applied"}}
	}

	j.logger.Info("judge: ReviewGoal LLM verdict",
		zap.Bool("pass", parsed.Pass),
		zap.Strings("reasons", parsed.Reasons),
	)
	return JudgeVerdict{Tier: "llm", Pass: parsed.Pass, Reasons: parsed.Reasons}
}

const goalReviewToolName = "emit_goal_review"

// goalReviewEmitTool defines the single structured-output tool the LLM
// must call. Schema keeps it minimal: pass/fail + up to 5 short reasons.
func goalReviewEmitTool() provider.ToolSchema {
	return provider.ToolSchema{
		Name:        goalReviewToolName,
		Description: "提交目标评审结论。这是你唯一可以调用的工具，调用后立即停止。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"pass", "reasons"},
			"properties": map[string]any{
				"pass": map[string]any{
					"type":        "boolean",
					"description": "true = 目标已达成；false = 目标未达成或有明显质量缺陷。",
				},
				"reasons": map[string]any{
					"type":        "array",
					"description": "pass=true 时留空数组；pass=false 时列出最多 5 条具体问题，每条不超过 100 字。",
					"maxItems":    5,
					"items": map[string]any{
						"type":      "string",
						"maxLength": 200,
					},
				},
			},
		},
	}
}

// buildGoalReviewSystemPrompt sets the evaluator role and output rules.
func buildGoalReviewSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString("你是计划执行质检员。\n\n")
	sb.WriteString("你的唯一职责：评审执行团队完成的步骤，判断是否真正达成了用户给出的目标。\n\n")
	sb.WriteString("判断标准：\n")
	sb.WriteString("- 目标的核心诉求是否被满足（不要求完美，但不能有明显遗漏）\n")
	sb.WriteString("- 每个步骤的产出摘要是否与目标方向吻合\n")
	sb.WriteString("- 不要因为步骤数量少或执行路径不同于你的预期而否定结果\n\n")
	sb.WriteString("硬性规则：只能调用 emit_goal_review 工具，不要输出任何其它正文。\n")
	return sb.String()
}

// buildGoalReviewUserMessage formats the goal and step results into a
// concise brief for the LLM evaluator.
func buildGoalReviewUserMessage(goal string, results []*StepResult) string {
	var sb strings.Builder
	sb.WriteString("用户目标：\n")
	sb.WriteString(strings.TrimSpace(goal))
	sb.WriteString("\n\n执行结果（各步骤摘要）：\n")
	for _, r := range results {
		if r == nil {
			continue
		}
		summary := strings.TrimSpace(r.Summary)
		if summary == "" {
			summary = "（无摘要）"
		} else {
			summary = truncForLog(summary, 300)
		}
		artCount := len(r.Artifacts)
		if artCount > 0 {
			fmt.Fprintf(&sb, "- 步骤 %s（%s，%d 份产出）：%s\n", r.StepID, r.Status, artCount, summary)
		} else {
			fmt.Fprintf(&sb, "- 步骤 %s（%s）：%s\n", r.StepID, r.Status, summary)
		}
	}
	sb.WriteString("\n请判断：上述执行结果是否真正满足了用户目标？")
	return sb.String()
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
// drive Judge from a submit_task_result outcome can do so without touching
// the schema package directly. Currently unused by the coordinator (the
// L3 driver already validates), kept so future Plan-mode hooks can call
// it before accepting a step's submission.
func validateSubmissionResult(schema, value map[string]any) []string {
	return submittool.ValidateAgainstSchema(schema, value)
}
