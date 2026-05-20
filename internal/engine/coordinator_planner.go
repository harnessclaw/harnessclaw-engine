package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
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

	// retryer, timeouts, logger are the optional engine-level
	// transport-retry layer. When set (via WithRetry), callOnce dispatches
	// through callLLM so the planner inherits the same network-retry
	// + heartbeat + retry-visibility behaviour as L1 emma / L3 sub-agents.
	// When nil, callOnce falls back to the legacy direct provider.Chat
	// path — preserved so existing test fakes that don't wire a retryer
	// still work.
	retryer  *retry.Retryer
	timeouts llmCallTimeouts
	logger   *zap.Logger
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

// WithRetry attaches the engine's shared Retryer + per-call timeouts +
// logger. Once set, planner LLM calls go through callLLM and pick
// up: exponential backoff for transient errors (429, 5xx, network
// blips), the FirstByte / API timeouts, the 30s heartbeat that keeps
// the surrounding agent card alive during long planner thinks, and the
// llm_retry wire events ("重试中 N/M") visible to the front-end.
//
// retryer nil = leave legacy direct-Chat path engaged. logger nil =
// zap.NewNop. Returning *LLMPlanner so it composes with the other
// With* setters in a fluent chain at construction time.
func (p *LLMPlanner) WithRetry(retryer *retry.Retryer, timeouts llmCallTimeouts, logger *zap.Logger) *LLMPlanner {
	p.retryer = retryer
	p.timeouts = timeouts
	if logger == nil {
		logger = zap.NewNop()
	}
	p.logger = logger
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
			feedback = "上一次尝试失败：" + err.Error() + "。请修正后重试。"
			continue
		}
		// Stamp Goal so callers (Scheduler, judge) get the original
		// task back even if the LLM dropped it from prompts.
		plan.Goal = strings.TrimSpace(in.Goal)
		if err := plan.Validate(); err != nil {
			lastErr = err
			feedback = "计划校验未通过（validation failed）：" + err.Error() +
				"。请重新生成合法的 DAG：step id 必须唯一（s1、s2…），depends_on 只能引用更早的 step id。"
			continue
		}
		if len(plan.Steps) > p.maxSteps {
			lastErr = fmt.Errorf("plan has %d steps, exceeds max %d", len(plan.Steps), p.maxSteps)
			feedback = fmt.Sprintf("步骤数过多（共 %d 步），请重新拆解为最多 %d 步。",
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
//
// When p.retryer is set (production path), the LLM call goes through
// callLLM — same code path L1 emma and L3 sub-agents use — so the
// planner inherits transport-level retry + backoff, heartbeats keeping
// the L2 specialists card alive, and "重试中" wire events. When nil
// (tests / minimal setups), the legacy direct-stream path is used.
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

	if p.retryer != nil {
		return p.callOnceWithRetry(ctx, req)
	}
	return p.callOnceLegacy(ctx, req)
}

// callOnceWithRetry routes through callLLM to get the full
// retry/heartbeat/visibility stack. AgentID and out are pulled from ctx
// (set by PlanCoordinator before invoking the planner) so heartbeats
// land on the L2 specialists agent card rather than nowhere.
func (p *LLMPlanner) callOnceWithRetry(ctx context.Context, req *provider.ChatRequest) (*Plan, string, error) {
	agentID, out := retryRoutingFromCtx(ctx)
	logger := p.logger
	if logger == nil {
		logger = zap.NewNop()
	}

	result := callLLM(ctx, p.provider, req, logger, p.retryer, p.timeouts, agentID, out, nil)
	if result == nil {
		return nil, "", fmt.Errorf("planner: nil retry result")
	}
	if result.streamErr != nil {
		return nil, "", fmt.Errorf("planner stream: %w", result.streamErr)
	}

	// Find the emit_plan tool call in the buffered results. Multiple
	// calls are tolerated (last wins) for symmetry with the legacy
	// path, even though the model is instructed to call exactly once.
	var toolInput string
	for _, tc := range result.toolCalls {
		if tc.Name == plannerEmitToolName {
			toolInput = tc.Input
		}
	}
	if toolInput == "" {
		return nil, "", fmt.Errorf("model did not call %q tool", plannerEmitToolName)
	}
	return parsePlanFromToolInput(toolInput)
}

// callOnceLegacy is the original direct-Chat path. Retained for tests
// and any caller that constructs an LLMPlanner without WithRetry.
func (p *LLMPlanner) callOnceLegacy(ctx context.Context, req *provider.ChatRequest) (*Plan, string, error) {
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
		Description: "提交拆解后的步骤 DAG。这是你唯一可以调用的工具，调用后立即停止。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"steps", "rationale"},
			"properties": map[string]any{
				"rationale": map[string]any{
					"type":        "string",
					"description": "用一句中文说明目标是如何被拆解的。",
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
								"type":        "string",
								"description": "用中文写一句简短的步骤标签（用于日志/可观测）。",
								"minLength":   1,
								"maxLength":   200,
							},
							"prompt": map[string]any{
								"type":        "string",
								"description": "用中文写出完整、自洽的执行指令，交给执行者就能直接开工。",
								"minLength":   1,
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
	sb.WriteString("你是一名资深的任务拆解规划员。\n\n")
	sb.WriteString("硬性规则：\n")
	sb.WriteString("- 只能通过调用 emit_plan 工具来回复，不要输出任何其它正文。\n")
	sb.WriteString("- 步骤数要匹配真实的拆解粒度：琐碎任务就 1 步，确实需要多阶段时才拆多步，不要凑数。\n")
	sb.WriteString(fmt.Sprintf("- 单个计划的步骤数不得超过 %d。\n", maxSteps))
	sb.WriteString("- step.id 按拓扑顺序命名为 \"s1\"、\"s2\"……\n")
	sb.WriteString("- depends_on 只能引用比当前步骤更早出现的 id，不允许成环，也不允许引用后面的步骤。\n")
	sb.WriteString("- 互相独立、可以并行执行的步骤要并列写出（depends_on 留空），不要为了让计划看起来串行而捏造假依赖。\n")
	sb.WriteString("- 每个步骤的 `prompt` 必须是给执行者的完整、自洽的任务说明，按交接给同事的语气来写，不要写成口号。\n")
	sb.WriteString("- 所有 description 与 prompt 一律使用中文，无论目标本身是什么语言。\n\n")

	sb.WriteString("步骤数参考标尺（按此校准粒度）：\n")
	sb.WriteString("- 「翻译这段话」/「X 的首都是哪」→ 1 步\n")
	sb.WriteString("- 「调研 X 并写一份报告」→ 2-3 步（调研 → 可选分析 → 撰写）\n")
	sb.WriteString("- 「调研 X、与 Y 对比、画决策矩阵、写报告」→ 4-5 步\n")
	sb.WriteString("- 「重构模块 X、补测试、写迁移文档」→ 4-6 步，可并行的部分要拆出并行分支\n\n")

	sb.WriteString("只关心每一步「做什么」，不要去决定「由谁来做」——执行者由下游在调度时再选。\n")
	return sb.String()
}

// buildPlannerUserMessage formats Goal + escalation + retry feedback
// into a single user message. feedback is non-empty on retry.
func buildPlannerUserMessage(in PlannerInput, feedback string) string {
	var sb strings.Builder
	sb.WriteString("目标：\n")
	sb.WriteString(strings.TrimSpace(in.Goal))

	if d := strings.TrimSpace(in.Description); d != "" {
		sb.WriteString("\n\n可观测标签：")
		sb.WriteString(d)
	}

	if in.Escalation != nil && !in.Escalation.IsEmpty() {
		sb.WriteString("\n\n--- 重新规划上下文 ---")
		sb.WriteString("\n上一次尝试已经产出了部分结果。请保留已有产物，只补充缺失的步骤，不要重复已完成的工作。")
	}

	if feedback != "" {
		sb.WriteString("\n\n--- 上一次尝试的反馈 ---\n")
		sb.WriteString(feedback)
	}

	return sb.String()
}

// subagentKeywords lists the strings each sub-agent type accepts as a
// signal that the goal is its kind of task. Substring match is intentional
// for Chinese (where 调研 / 代码 etc. are unambiguous tokens) but tight
// for English — see the `developer` block: 'code' / 'go ' / 'script' /
// 'function' were dropped because they triggered on `codex` / `vscode` /
// `encode` / `decode` / English connector words / `transcript` /
// `functional analysis` etc., causing research and analysis tasks to be
// misrouted to developer.
var subagentKeywords = map[string][]string{
	"writer":         {"write ", "draft", "polish", "translate", "邮件", "翻译", "润色", "撰写"},
	"researcher":     {"research", "find out", "调研", "查一下", "搜集"},
	"analyst":        {"analyze", "compare", "对比", "分析"},
	"developer":      {"代码", "脚本", "中间件", "调试", "middleware", "debug", "python", "typescript", "javascript", "compile"},
	"travel_planner": {"travel", "trip", "itinerary", "出行", "行程"},
	"recommender":    {"recommend", "best", "pick", "推荐", "选购", "比价"},
	"scheduler":      {"schedule", "calendar", "日程", "排期", "会议"},
}

// matchesSubagent reports whether goal matches any keyword for subagent.
// Kept as a thin wrapper so callers that only need yes/no don't pay for
// the score computation.
func matchesSubagent(goal, subagent string) bool {
	return countSubagentMatches(goal, subagent) > 0
}

// countSubagentMatches returns how many of subagent's keywords appear in
// goal. Used by the resolver to break ties between candidates whose
// keyword lists overlap (e.g. "调研代码架构" matches both researcher and
// developer; the higher-count one wins).
func countSubagentMatches(goal, subagent string) int {
	keywords := subagentKeywords[subagent]
	n := 0
	for _, k := range keywords {
		if strings.Contains(goal, k) {
			n++
		}
	}
	return n
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
