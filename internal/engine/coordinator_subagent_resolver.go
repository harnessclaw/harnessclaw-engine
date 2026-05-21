package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/pkg/types"
)

// SubagentResolver decides which L3 sub-agent runs a given step at
// dispatch time. Replaces the v1.15- behaviour where the Planner pre-
// bound each step to a sub-agent type — the new model lets Plan focus
// on "what to do" while keeping "who does it" as a runtime concern.
//
// Implementations:
//   - HeuristicSubagentResolver: zero-LLM keyword matcher (default)
//   - LLMSubagentResolver: stub for a future structured-output LLM
//     decision; falls through to heuristic until wired
//
// The Scheduler calls Resolve before every step dispatch; if PlanStep.
// SubagentType was already set (e.g. by an LLM Planner that produced
// an executor-aware plan), the Scheduler short-circuits and uses it
// directly without consulting the resolver.
type SubagentResolver interface {
	// Resolve returns the sub-agent name to dispatch plus a short
	// rationale for telemetry. Must always return a name from
	// available — implementations fall back rather than fail when no
	// rule matches.
	//
	// goal is typically PlanStep.Prompt or PlanStep.Description; when
	// both are empty the implementation should fall back to a generic
	// agent (general-purpose / first available).
	Resolve(ctx context.Context, goal string, available []string) (subagent string, reason string, err error)
}

// HeuristicSubagentResolver applies the same keyword rules previously
// embedded in HeuristicPlanner. Pure function of (goal, available); no
// LLM, no I/O.
type HeuristicSubagentResolver struct{}

// NewHeuristicSubagentResolver returns the default resolver.
func NewHeuristicSubagentResolver() *HeuristicSubagentResolver {
	return &HeuristicSubagentResolver{}
}

// Resolve picks a sub-agent by SCORING each candidate's keyword
// matches against the goal text, then breaking ties by a priority
// order that puts task-VERB roles (research / analyze / write) ahead
// of task-OBJECT roles (developer).
//
// Why scoring instead of first-match-wins: a goal like "调研 OpenClaw
// 的代码架构" matches both researcher (verb "调研") and developer
// (object "代码"). The previous first-match-wins + developer-first
// order returned developer for this clearly-research task. The new
// rule: highest match count wins; on a tie, verb roles beat object
// roles, so the same goal correctly routes to researcher.
//
// Priority order (lower index = preferred on tie):
//   1. researcher  — "find out / 调研" verb tasks
//   2. analyst     — "compare / 分析" verb tasks
//   3. writer      — "撰写 / draft" verb tasks
//   4. travel_planner / recommender / scheduler — domain specialists
//   5. developer   — software-action object tasks; intentionally last
//                    because developer keywords (代码 / 脚本) often
//                    appear as the OBJECT of a research / analysis verb
func (r *HeuristicSubagentResolver) Resolve(_ context.Context, goal string, available []string) (string, string, error) {
	if len(available) == 0 {
		return "", "", fmt.Errorf("subagent resolver: no available sub-agents")
	}
	set := newSubagentSet(available)
	lower := strings.ToLower(strings.TrimSpace(goal))

	if lower == "" {
		return fallbackSubagent(set, available), "fallback: empty goal", nil
	}

	// Heuristic resolver's keyword routing was tied to the 7 fixed L3
	// workers (writer/researcher/analyst/developer/travel_planner/
	// recommender/scheduler) which have been removed. freelancer is the
	// only remaining L3 and is keyword-agnostic. Leaving the priority
	// list empty makes the loop below a no-op so resolution always
	// falls through to fallbackSubagent — which is the correct
	// behaviour now.
	priority := []string{}

	bestName := ""
	bestScore := 0
	bestPriority := len(priority) // smaller = better
	for i, candidate := range priority {
		if !set.has(candidate) {
			continue
		}
		score := countSubagentMatches(lower, candidate)
		if score == 0 {
			continue
		}
		// Strictly higher score wins; same score → earlier priority wins.
		if score > bestScore || (score == bestScore && i < bestPriority) {
			bestName = candidate
			bestScore = score
			bestPriority = i
		}
	}
	if bestName != "" {
		return bestName, fmt.Sprintf("matched %d keyword(s) for %q", bestScore, bestName), nil
	}

	// No candidate matched any keyword — fall back.
	pick := fallbackSubagent(set, available)
	return pick, "no keyword match; defaulting to fallback", nil
}

// fallbackSubagent encapsulates the "no rule matched" priority: prefer
// freelancer (the user-skill-driven L3, capable of any task once an
// appropriate skill is loaded), then the legacy general-purpose
// coordinator if it's still registered, then whatever is first.
func fallbackSubagent(set subagentSet, available []string) string {
	if set.has("freelancer") {
		return "freelancer"
	}
	if set.has("general-purpose") {
		return "general-purpose"
	}
	return available[0]
}

// LLMSubagentResolver drives sub-agent selection via a structured-output
// LLM call. The model receives the goal text and a description of every
// available sub-agent (name + description + skills + example tasks) and
// returns one pick from a strict enum. Replaces keyword-table guessing
// with semantic understanding — handles user-defined sub-agents the
// heuristic resolver doesn't know about.
//
// Falls back to a delegate resolver (heuristic by default) when the
// LLM call fails, returns an out-of-set pick, or never invokes the
// tool. The fallback path is the production safety net: under network
// blip / model unavailability, plan execution continues with a
// reasonable guess rather than blocking.
type LLMSubagentResolver struct {
	provider provider.Provider
	model    string
	registry *agent.AgentDefinitionRegistry
	fallback SubagentResolver
	logger   *zap.Logger

	maxTokens int

	// retryer + timeouts: when set (via WithRetry), the LLM call goes
	// through callLLM and picks up backoff retry, heartbeats, and
	// retry-status wire events. When nil, falls back to direct
	// provider.Chat. Kept optional so test setups that pass only
	// (provider, model, registry, fallback, logger) still work.
	retryer  *retry.Retryer
	timeouts llmCallTimeouts
}

// llmResolverDefaults — kept private so tweaking them doesn't pollute
// the package's public surface.
const (
	llmResolverDefaultMaxTokens = 256
	llmResolverToolName         = "select_subagent"
)

// NewLLMSubagentResolver builds an LLM-backed resolver. Required:
// provider (drives the Chat call), model (LLM identifier), registry
// (looks up rich metadata for each available name). fallback is what
// runs when the LLM path fails — pass nil to use the heuristic
// resolver. logger may be nil (uses zap.NewNop).
//
// When provider or registry is nil the resolver degrades silently to
// fallback only, so test setups that don't wire one in still work.
func NewLLMSubagentResolver(
	prov provider.Provider,
	model string,
	registry *agent.AgentDefinitionRegistry,
	fallback SubagentResolver,
	logger *zap.Logger,
) *LLMSubagentResolver {
	if fallback == nil {
		fallback = NewHeuristicSubagentResolver()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LLMSubagentResolver{
		provider:  prov,
		model:     model,
		registry:  registry,
		fallback:  fallback,
		logger:    logger,
		maxTokens: llmResolverDefaultMaxTokens,
	}
}

// WithRetry attaches the engine's shared Retryer and per-call timeouts
// so resolver LLM calls inherit the same retry/heartbeat/visibility
// stack as L1 emma / L3 sub-agents. Mirror of LLMPlanner.WithRetry —
// see that doc for the full rationale.
func (r *LLMSubagentResolver) WithRetry(retryer *retry.Retryer, timeouts llmCallTimeouts) *LLMSubagentResolver {
	r.retryer = retryer
	r.timeouts = timeouts
	return r
}

// Resolve asks the LLM to pick one sub-agent from available. On success,
// the returned reason is prefixed with "(LLM) " so logs distinguish
// model picks from heuristic / fallback picks. On any failure path
// (provider nil, LLM call error, model didn't call the tool, model
// picked an out-of-set name) it delegates to the fallback resolver
// with the reason explaining why.
func (r *LLMSubagentResolver) Resolve(ctx context.Context, goal string, available []string) (string, string, error) {
	if r.provider == nil || len(available) == 0 {
		return r.fallbackWith(ctx, goal, available, "no LLM provider")
	}
	if len(available) == 1 {
		// Single option — no need to spend an LLM call.
		return available[0], "only one sub-agent available", nil
	}

	pick, rationale, err := r.callOnce(ctx, goal, available)
	if err != nil {
		r.logger.Warn("LLM subagent resolver: call failed; falling back",
			zap.String("goal_preview", truncForLog(goal, 120)),
			zap.Error(err),
		)
		return r.fallbackWith(ctx, goal, available, "LLM error: "+err.Error())
	}

	if !newSubagentSet(available).has(pick) {
		r.logger.Warn("LLM subagent resolver: model picked out-of-set; falling back",
			zap.String("picked", pick),
			zap.Strings("available", available),
		)
		return r.fallbackWith(ctx, goal, available,
			fmt.Sprintf("LLM picked invalid %q", pick))
	}
	return pick, "(LLM) " + rationale, nil
}

// fallbackWith runs the delegate resolver and prefixes the reason with
// the LLM-failure cause so operators can see WHY we ended up using the
// heuristic.
func (r *LLMSubagentResolver) fallbackWith(ctx context.Context, goal string, available []string, why string) (string, string, error) {
	pick, reason, err := r.fallback.Resolve(ctx, goal, available)
	if err != nil {
		return "", "", err
	}
	return pick, why + "; fallback: " + reason, nil
}

// callOnce sends the structured-output prompt and parses the result.
// Returns (pick, rationale, nil) on a clean tool call, error otherwise.
//
// When r.retryer is set (production path), dispatch goes through
// callLLM for transport-level retry + heartbeat + visibility.
// Otherwise the legacy direct-Chat path runs (tests / minimal setups).
func (r *LLMSubagentResolver) callOnce(ctx context.Context, goal string, available []string) (string, string, error) {
	listings := r.lookupListings(available)
	system := buildResolverSystemPrompt()
	user := buildResolverUserMessage(goal, listings)

	req := &provider.ChatRequest{
		Model:  r.model,
		System: system,
		Messages: []types.Message{{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: user}},
		}},
		Tools:       []provider.ToolSchema{resolverEmitTool(available)},
		MaxTokens:   r.maxTokens,
		Temperature: 0,
	}

	var toolInput string
	if r.retryer != nil {
		// Production: callLLM buffers tool_use into result.toolCalls;
		// we scan for select_subagent and pick the last one. agentID + out
		// come from ctx (set by the Scheduler before dispatch).
		agentID, out := retryRoutingFromCtx(ctx)
		result := callLLM(ctx, r.provider, req, r.logger, r.retryer, r.timeouts, agentID, out, out)
		if result == nil {
			return "", "", fmt.Errorf("resolver: nil retry result")
		}
		if result.streamErr != nil {
			return "", "", fmt.Errorf("stream: %w", result.streamErr)
		}
		for _, tc := range result.toolCalls {
			if tc.Name == llmResolverToolName {
				toolInput = tc.Input
			}
		}
	} else {
		stream, err := r.provider.Chat(ctx, req)
		if err != nil {
			return "", "", fmt.Errorf("provider chat: %w", err)
		}
		for ev := range stream.Events {
			if ev.Type == types.StreamEventToolUse && ev.ToolCall != nil &&
				ev.ToolCall.Name == llmResolverToolName {
				toolInput = ev.ToolCall.Input
			}
		}
		if err := stream.Err(); err != nil {
			return "", "", fmt.Errorf("stream: %w", err)
		}
	}
	if toolInput == "" {
		return "", "", fmt.Errorf("model did not call %q", llmResolverToolName)
	}

	var parsed struct {
		SubagentType string `json:"subagent_type"`
		Rationale    string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(toolInput), &parsed); err != nil {
		return "", "", fmt.Errorf("parse tool input: %w", err)
	}
	if parsed.SubagentType == "" {
		return "", "", fmt.Errorf("empty subagent_type in tool input")
	}
	rationale := strings.TrimSpace(parsed.Rationale)
	if rationale == "" {
		rationale = "no rationale supplied"
	}
	return parsed.SubagentType, rationale, nil
}

// lookupListings fetches PlannerListing for each name in available,
// preserving order. Names absent from the registry get a minimal stub
// (just the name) so the model still sees them in the candidate list.
func (r *LLMSubagentResolver) lookupListings(available []string) []agent.PlannerListing {
	if r.registry == nil {
		out := make([]agent.PlannerListing, len(available))
		for i, n := range available {
			out[i] = agent.PlannerListing{Name: n}
		}
		return out
	}
	all := r.registry.ListForPlanner()
	byName := make(map[string]agent.PlannerListing, len(all))
	for _, l := range all {
		byName[l.Name] = l
	}
	out := make([]agent.PlannerListing, 0, len(available))
	for _, n := range available {
		if l, ok := byName[n]; ok {
			out = append(out, l)
		} else {
			out = append(out, agent.PlannerListing{Name: n})
		}
	}
	return out
}

// resolverEmitTool builds the strict tool schema. The enum constraint
// on subagent_type lets the provider reject out-of-set picks at the
// schema layer (some providers do; others don't — we still validate
// post-hoc in Resolve).
func resolverEmitTool(available []string) provider.ToolSchema {
	enumValues := make([]any, len(available))
	for i, n := range available {
		enumValues[i] = n
	}
	return provider.ToolSchema{
		Name:        llmResolverToolName,
		Description: "选定最适合执行该任务的 sub-agent。这是你唯一可以调用的工具，调用后立即停止。",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"subagent_type", "rationale"},
			"properties": map[string]any{
				"subagent_type": map[string]any{
					"type":        "string",
					"description": "从候选列表中选择一个 sub-agent 名称。",
					"enum":        enumValues,
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "用一句中文说明为什么选这个 sub-agent。",
					"minLength":   1,
					"maxLength":   200,
				},
			},
		},
	}
}

// buildResolverSystemPrompt — kept terse: the work of explaining each
// candidate is in the user message; the system block just sets the
// task framing and output discipline.
func buildResolverSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString("你是一个任务派发助手。\n\n")
	sb.WriteString("规则：\n")
	sb.WriteString("- 只能通过调用 select_subagent 工具来回复，不要输出其它正文。\n")
	sb.WriteString("- subagent_type 必须从候选列表中精确选择一个，不要拼写偏差。\n")
	sb.WriteString("- 关注任务的「动词」（调研 / 分析 / 撰写 / 实现）和「目标产物」，而不是单纯的关键字。\n")
	sb.WriteString("- 任务描述里出现「代码 / 脚本」不一定是开发任务——可能是「调研代码架构」这种研究任务。\n")
	sb.WriteString("- 给出一句中文 rationale 说明你的判断依据。\n")
	return sb.String()
}

// buildResolverUserMessage assembles the goal + agent inventory the
// model needs. Each agent gets its description / skills / example
// tasks so the model can match capabilities semantically rather than
// reading names.
func buildResolverUserMessage(goal string, listings []agent.PlannerListing) string {
	var sb strings.Builder
	sb.WriteString("任务：\n")
	sb.WriteString(strings.TrimSpace(goal))
	sb.WriteString("\n\n候选 sub-agent：\n")
	for _, l := range listings {
		sb.WriteString("\n- ")
		sb.WriteString(l.Name)
		if l.DisplayName != "" && l.DisplayName != l.Name {
			sb.WriteString(" (")
			sb.WriteString(l.DisplayName)
			sb.WriteString(")")
		}
		if l.Description != "" {
			sb.WriteString("\n  描述：")
			sb.WriteString(l.Description)
		}
		if len(l.Skills) > 0 {
			sb.WriteString("\n  技能：")
			sb.WriteString(strings.Join(l.Skills, "、"))
		}
		if len(l.ExampleTasks) > 0 {
			sb.WriteString("\n  示例任务：")
			sb.WriteString(strings.Join(l.ExampleTasks, "；"))
		}
		if len(l.Limitations) > 0 {
			sb.WriteString("\n  限制：")
			sb.WriteString(strings.Join(l.Limitations, "；"))
		}
	}
	sb.WriteString("\n\n从上面的候选里选出最适合的一个，并说明理由。")
	return sb.String()
}

