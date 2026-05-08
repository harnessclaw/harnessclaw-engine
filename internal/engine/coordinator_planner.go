package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
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

// Planner produces a Plan from a PlannerInput. The single production
// implementation is LLMPlanner; the interface stays so tests can swap
// in fakes without pulling in a real provider.
type Planner interface {
	Plan(ctx context.Context, in PlannerInput) (*PlannerOutput, error)
}

// LLMPlanner decomposes the goal by asking an LLM to fill in a strict
// JSON schema (the `emit_plan` tool). Replaces the v1.16 keyword-driven
// HeuristicPlanner whose hard-coded "research+write → 2 steps" /
// "research+analyze → 2 steps" rules made every plan look the same
// regardless of actual task complexity.
//
// Strategy: the planner exposes a single tool to the model and instructs
// it to call exactly that tool. Tool-call mode is the most reliable
// structured-output path — schema validation happens at the provider
// boundary, not in fragile post-hoc text parsing.
//
// On validation failure the planner retries up to maxAttempts, feeding
// the failure reason back so the model can self-correct (typical case:
// it forgot to honour depends_on ordering and produces a forward
// reference).
type LLMPlanner struct {
	provider    provider.Provider
	model       string
	maxAttempts int
	maxTokens   int
	maxSteps    int
}

// Sensible defaults; LLMPlanner-specific (kept private to avoid
// polluting other planner types if they ever land).
const (
	plannerDefaultMaxAttempts = 3
	plannerDefaultMaxTokens   = 4096
	plannerDefaultMaxSteps    = 12
)

// NewLLMPlanner constructs an LLMPlanner. provider must not be nil;
// model is the LLM model id (e.g. "claude-sonnet-4-6"). MaxAttempts /
// MaxTokens / MaxSteps fall back to defaults; tweak via the With* setters
// if needed. Temperature is fixed at 0 to keep decompositions stable
// across retries — the only signal a reasonable LLM uses to decompose
// is the goal text + available subagents, not stochastic creativity.
func NewLLMPlanner(p provider.Provider, model string) *LLMPlanner {
	return &LLMPlanner{
		provider:    p,
		model:       model,
		maxAttempts: plannerDefaultMaxAttempts,
		maxTokens:   plannerDefaultMaxTokens,
		maxSteps:    plannerDefaultMaxSteps,
	}
}

// WithMaxAttempts overrides the retry cap.
func (p *LLMPlanner) WithMaxAttempts(n int) *LLMPlanner {
	if n > 0 {
		p.maxAttempts = n
	}
	return p
}

// WithMaxSteps overrides the per-plan upper bound on step count.
func (p *LLMPlanner) WithMaxSteps(n int) *LLMPlanner {
	if n > 0 {
		p.maxSteps = n
	}
	return p
}

// Plan implements Planner. Drives the LLM call + validate + retry loop.
// Returns the validated Plan or an error after exhausting maxAttempts.
//
// Design: the planner decomposes the goal into "what to do" — it does
// NOT pick the executor. PlanStep.SubagentType is left empty by design;
// the Scheduler resolves it at dispatch time via SubagentResolver,
// which can react to dynamic skill availability and runtime conditions
// the planner doesn't see. Putting AvailableSubagents into the prompt
// would couple the plan to a specific roster and lose this flexibility
// — the planner stays roster-agnostic on purpose.
//
// Pre-condition: non-empty Goal. AvailableSubagents on PlannerInput is
// kept for backward compat with callers that still pass it, but the
// LLMPlanner ignores it.
func (p *LLMPlanner) Plan(ctx context.Context, in PlannerInput) (*PlannerOutput, error) {
	if strings.TrimSpace(in.Goal) == "" {
		return nil, errors.New("planner: empty goal")
	}
	if p.provider == nil {
		return nil, errors.New("planner: provider is nil")
	}

	var lastErr error
	var feedback string

	for attempt := 0; attempt < p.maxAttempts; attempt++ {
		plan, rationale, err := p.callOnce(ctx, in, feedback)
		if err != nil {
			lastErr = err
			feedback = "Previous attempt failed: " + err.Error() + ". Fix and retry."
			continue
		}
		// Stamp Goal so callers (Scheduler, judge) get the original
		// task back even if the LLM dropped it from prompts.
		plan.Goal = strings.TrimSpace(in.Goal)
		if err := plan.Validate(); err != nil {
			lastErr = err
			feedback = "Plan validation failed: " + err.Error() +
				". Re-emit a valid DAG: unique step ids (s1, s2, ...), depends_on must reference earlier ids only."
			continue
		}
		if len(plan.Steps) > p.maxSteps {
			lastErr = fmt.Errorf("plan has %d steps, exceeds max %d", len(plan.Steps), p.maxSteps)
			feedback = fmt.Sprintf("Plan has too many steps (%d). Decompose more carefully into at most %d steps.",
				len(plan.Steps), p.maxSteps)
			continue
		}
		return &PlannerOutput{Plan: plan, Rationale: rationale}, nil
	}
	return nil, fmt.Errorf("planner: exhausted %d attempts: %w", p.maxAttempts, lastErr)
}

// callOnce sends one chat request and parses the emit_plan tool input.
// Stream-level errors (network, provider failures) bubble up unchanged;
// "model didn't call the tool" / "input is not JSON" become validation-
// retryable errors handled by Plan.
func (p *LLMPlanner) callOnce(ctx context.Context, in PlannerInput, feedback string) (*Plan, string, error) {
	req := &provider.ChatRequest{
		Model:       p.model,
		System:      buildPlannerSystemPrompt(p.maxSteps),
		Messages:    []types.Message{{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: buildPlannerUserMessage(in, feedback)}},
		}},
		Tools:       []provider.ToolSchema{plannerEmitTool(p.maxSteps)},
		MaxTokens:   p.maxTokens,
		Temperature: 0,
	}

	stream, err := p.provider.Chat(ctx, req)
	if err != nil {
		return nil, "", fmt.Errorf("provider chat: %w", err)
	}

	var (
		toolInput string
		streamErr error
	)
	for ev := range stream.Events {
		if ev.Type == types.StreamEventToolUse && ev.ToolCall != nil && ev.ToolCall.Name == plannerEmitToolName {
			// Last emit_plan call wins (the model should only call once,
			// but be defensive against a retried call within the same
			// stream).
			toolInput = ev.ToolCall.Input
		}
		if ev.Type == types.StreamEventError && ev.Error != nil {
			streamErr = ev.Error
		}
	}
	if err := stream.Err(); err != nil {
		return nil, "", fmt.Errorf("planner stream: %w", err)
	}
	if streamErr != nil {
		return nil, "", fmt.Errorf("planner stream event: %w", streamErr)
	}
	if toolInput == "" {
		return nil, "", fmt.Errorf("model did not call %q tool", plannerEmitToolName)
	}

	return parsePlanFromToolInput(toolInput)
}

// plannerEmitToolName is the single tool name the planner LLM is asked
// to call. Kept as a constant so test fakes can target it precisely.
const plannerEmitToolName = "emit_plan"

// plannerEmitTool builds the JSON schema for the planner's structured
// output. The model must call this tool exactly once; its `input` is
// what we parse back into a Plan.
//
// The schema deliberately does NOT include subagent_type. Choosing the
// executor is the SubagentResolver's job at dispatch time, where it
// can react to runtime skill availability and re-spawn conditions the
// planner doesn't see. Letting the planner pick would couple every
// plan to a static roster snapshot and undermine dynamic skill
// injection.
func plannerEmitTool(maxSteps int) provider.ToolSchema {
	return provider.ToolSchema{
		Name: plannerEmitToolName,
		Description: "Submit the planned step DAG. " +
			"This is the ONLY tool you may call. After calling it, stop.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"steps", "rationale"},
			"properties": map[string]any{
				"rationale": map[string]any{
					"type":        "string",
					"description": "One-line explanation of how the goal was decomposed.",
					"minLength":   1,
					"maxLength":   400,
				},
				"steps": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": maxSteps,
					"items": map[string]any{
						"type":     "object",
						"required": []string{"id", "description", "prompt"},
						"properties": map[string]any{
							"id": map[string]any{
								"type":    "string",
								"pattern": `^s\d+$`,
							},
							"description": map[string]any{
								"type":      "string",
								"minLength": 1,
								"maxLength": 200,
							},
							"prompt": map[string]any{
								"type":      "string",
								"minLength": 1,
							},
							"depends_on": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}
}

// parsePlanFromToolInput unmarshals the model's emit_plan tool input
// JSON into a Plan + the rationale string. JSON shape is locked by the
// tool's InputSchema, so this is mostly defensive.
//
// Note: subagent_type is intentionally NOT parsed — the planner is
// roster-agnostic by design (executor selection happens at dispatch).
// Even if a future schema variant adds the field, ignoring it here
// keeps the boundary clean.
func parsePlanFromToolInput(raw string) (*Plan, string, error) {
	var parsed struct {
		Rationale string `json:"rationale"`
		Steps     []struct {
			ID          string   `json:"id"`
			Description string   `json:"description"`
			Prompt      string   `json:"prompt"`
			DependsOn   []string `json:"depends_on,omitempty"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, "", fmt.Errorf("parse emit_plan input: %w", err)
	}
	plan := &Plan{Steps: make([]*PlanStep, 0, len(parsed.Steps))}
	for _, s := range parsed.Steps {
		plan.Steps = append(plan.Steps, &PlanStep{
			ID:          s.ID,
			Description: s.Description,
			Prompt:      s.Prompt,
			DependsOn:   s.DependsOn,
			// SubagentType deliberately left empty — Scheduler picks.
		})
	}
	return plan, parsed.Rationale, nil
}

// buildPlannerSystemPrompt assembles the role + rules. Hard rules are
// reinforced by the JSON schema; the step-count guide is the LLM's
// main signal for "how many steps".
//
// Roster-agnostic by design: the prompt does NOT mention sub-agents,
// skills, or any specific roles. The LLM decomposes work in the
// abstract; the runtime SubagentResolver picks the executor at
// dispatch time based on each step's description / prompt and the
// dynamically-injected skill catalog.
func buildPlannerSystemPrompt(maxSteps int) string {
	var sb strings.Builder
	sb.WriteString("You are a senior task-decomposition planner.\n\n")
	sb.WriteString("Hard rules:\n")
	sb.WriteString("- Respond ONLY by calling the emit_plan tool. Do not write any other text.\n")
	sb.WriteString("- Match step count to actual decomposition: 1 step for trivial goals, more for genuinely multi-stage work. Do not pad.\n")
	sb.WriteString(fmt.Sprintf("- Never exceed %d steps in one plan.\n", maxSteps))
	sb.WriteString("- step.id is \"s1\", \"s2\", ... in topological order.\n")
	sb.WriteString("- depends_on MUST reference earlier step ids only. No cycles, no forward references.\n")
	sb.WriteString("- Use parallel branches (omit depends_on) for steps that can run independently. Do not invent fake dependencies just to make the plan look sequential.\n")
	sb.WriteString("- Each step's `prompt` is a complete, self-contained task description for whoever executes it — write it as you would brief a teammate, not as a slogan.\n\n")

	sb.WriteString("Step-count guide (calibrate against this):\n")
	sb.WriteString("- \"Translate this paragraph\" / \"What's the capital of X\" → 1 step\n")
	sb.WriteString("- \"Research X and write a report\" → 2-3 steps (research → optional analyze → draft)\n")
	sb.WriteString("- \"Research X, compare with Y, draw a decision matrix, write the report\" → 4-5 steps\n")
	sb.WriteString("- \"Refactor module X, add tests, write migration doc\" → 4-6 steps with parallel branches when possible\n\n")

	sb.WriteString("Focus only on WHAT each step does, not WHO executes it. Picking the executor is a downstream concern handled at dispatch time.\n")
	return sb.String()
}

// buildPlannerUserMessage formats Goal + escalation + retry feedback
// into a single user message. feedback is non-empty on retry.
func buildPlannerUserMessage(in PlannerInput, feedback string) string {
	var sb strings.Builder
	sb.WriteString("Goal:\n")
	sb.WriteString(strings.TrimSpace(in.Goal))

	if d := strings.TrimSpace(in.Description); d != "" {
		sb.WriteString("\n\nObservability label: ")
		sb.WriteString(d)
	}

	if in.Escalation != nil && !in.Escalation.IsEmpty() {
		sb.WriteString("\n\n--- Re-plan context ---")
		sb.WriteString("\nA previous attempt produced partial results. Preserve already-produced artifacts; only schedule the missing pieces. Avoid redoing completed work.")
	}

	if feedback != "" {
		sb.WriteString("\n\n--- Previous attempt feedback ---\n")
		sb.WriteString(feedback)
	}

	return sb.String()
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
