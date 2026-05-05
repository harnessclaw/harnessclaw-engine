// Package orchestrate implements the Phase-2 Orchestrate tool.
//
// emma calls this tool with a single `intent` string. The tool internally:
//   1. Spawns a Planner sub-agent that converts intent → structured plan JSON.
//   2. Validates the plan (≤ 10 steps, no cycles, known subagent types).
//   3. On Planner failure, retries up to 2 times. After the third failure
//      it gives up and returns { status: "plan_failed", degraded: true } so
//      emma can fall back to serial Agent calls (Phase-1 behaviour).
//   4. On success, hands the plan to PlanExecutor for parallel DAG execution.
//   5. Returns { status, steps, deliverables } back to emma.
//
// Design reference: docs/design/architecture/layered-architecture.md (§七、§八、§九).
package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	exec "harnessclaw-go/internal/engine/orchestrate"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the LLM-facing tool identifier.
const ToolName = "Orchestrate"

// PlannerSubagentType is the subagent_type used when spawning the Planner.
// It maps to PlannerProfile in internal/engine/prompt.
const PlannerSubagentType = "planner"

// MaxPlannerAttempts caps how many times the Orchestrate tool will ask the
// Planner for a plan. After this many failures it returns plan_failed with
// degraded:true so emma can fall back to Phase-1 serial Agent dispatch.
const MaxPlannerAttempts = 3 // 1 initial + 2 retries (matches doc §八)

// AgentRoster supplies the Planner with the available sub-agent inventory.
// main.go wires this from the agent-definition registry plus the built-in
// profile names.
type AgentRoster interface {
	// AvailableSubagentTypes returns agent names for the allowed-set check.
	AvailableSubagentTypes() []string
	// ListForPlanner returns the rich per-agent listing (description,
	// limitations, example tasks, cost tier) the Planner uses to pick
	// the right agent. May return nil — buildPlannerPrompt falls back to
	// the name-only format when it does.
	ListForPlanner() []agent.PlannerListing
}

// staticRoster lets callers / tests inject a fixed list.
type staticRoster []string

func (s staticRoster) AvailableSubagentTypes() []string    { return []string(s) }
func (s staticRoster) ListForPlanner() []agent.PlannerListing { return nil }

// NewStaticRoster builds an AgentRoster from a slice of names — handy for tests.
func NewStaticRoster(names []string) AgentRoster { return staticRoster(names) }

// OrchestrateTool implements the Tool interface for the Orchestrate slot.
type OrchestrateTool struct {
	tool.BaseTool
	spawner  agent.AgentSpawner
	executor *exec.PlanExecutor
	roster   AgentRoster
	logger   *zap.Logger
}

// New builds an OrchestrateTool. spawner runs the Planner and step agents;
// roster (optional) provides the allowed subagent_type list.
func New(spawner agent.AgentSpawner, roster AgentRoster, logger *zap.Logger) *OrchestrateTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrchestrateTool{
		spawner:  spawner,
		executor: exec.NewPlanExecutor(spawner, logger),
		roster:   roster,
		logger:   logger,
	}
}

func (t *OrchestrateTool) Name() string             { return ToolName }
func (t *OrchestrateTool) Description() string      { return orchestrateDescription }
func (t *OrchestrateTool) IsReadOnly() bool         { return false }
func (t *OrchestrateTool) IsConcurrencySafe() bool  { return false }
func (t *OrchestrateTool) IsLongRunning() bool      { return true }
func (t *OrchestrateTool) InputSchema() map[string]any {
	return inputSchema
}

// CheckPermission auto-allows — Orchestrate just spawns sub-agents, which
// themselves go through the normal permission pipeline.
func (t *OrchestrateTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *OrchestrateTool) ValidateInput(raw json.RawMessage) error {
	in, err := parseInput(raw)
	if err != nil {
		return err
	}
	return in.validate()
}

// InterruptBehavior cancels in-flight execution on user interrupt.
func (t *OrchestrateTool) InterruptBehavior() tool.InterruptMode { return tool.InterruptCancel }

// MaxResultSizeChars caps the JSON payload size returned to emma.
func (t *OrchestrateTool) MaxResultSizeChars() int { return 50000 }

// Execute is the LLM-callable entry point.
func (t *OrchestrateTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	startTime := time.Now()

	in, err := parseInput(raw)
	if err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}
	if err := in.validate(); err != nil {
		return errResult(err.Error()), nil
	}

	parentSessionID := ""
	var parentOut chan<- types.EngineEvent
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		parentSessionID = tuc.Core.SessionID
	}
	if out, ok := tool.GetEventOut(ctx); ok {
		parentOut = out
	}

	available := t.resolveAgents(in.AvailableAgents)
	if len(available) == 0 {
		t.logger.Warn("orchestrate invoked with empty agent roster")
	}
	allowedSet := toSet(available)

	t.logger.Info("orchestrate begin",
		zap.String("intent", in.Intent),
		zap.Strings("available_agents", available),
	)

	// --- Plan phase: ask the Planner up to MaxPlannerAttempts times. ---
	listings := t.roster.ListForPlanner()
	plan, plannerErr := t.runPlanner(ctx, in.Intent, available, listings, parentSessionID, parentOut, allowedSet)
	if plan == nil {
		// Degrade: tell emma to fall back to serial Agent.
		t.logger.Warn("orchestrate degrading to phase-1",
			zap.Error(plannerErr),
			zap.Duration("duration", time.Since(startTime)),
		)
		payload := map[string]any{
			"status":   "plan_failed",
			"degraded": true,
			"message":  "任务拆解失败，请你自己分步安排",
			"reason":   strings.TrimSpace(errMessage(plannerErr)),
		}
		return jsonResult(payload, map[string]any{
			"render_hint": "orchestrate",
			"degraded":    true,
			"duration_ms": time.Since(startTime).Milliseconds(),
		}, false), nil
	}

	t.logger.Info("orchestrate plan accepted",
		zap.Int("steps", len(plan.Steps)),
	)

	// --- Execution phase: run the DAG. ---
	// Lift the surrounding trace context so plan.* / task.* events ride
	// the same trace as the rest of the request. When the engine didn't
	// attach a trace (e.g. unit tests calling the tool directly), emit
	// remains a no-op inside the executor.
	execOpts := exec.ExecuteOptions{
		ParentSessionID: parentSessionID,
		ParentOut:       parentOut,
		PlanGoal:        in.Intent,
		PlanAgentID:     "plan_agent",
		PlanAgentRunID:  emit.NewAgentRunID(),
	}
	if tc := emit.FromContext(ctx); tc != nil {
		execOpts.TraceID = tc.TraceID
		execOpts.ParentEventID = tc.ParentEventID
		execOpts.ParentTaskID = tc.ParentTaskID
		execOpts.Sequencer = tc.Sequencer
	}
	res := t.executor.Execute(ctx, plan, execOpts)

	// Emit deliverable events for client UIs.
	if parentOut != nil {
		for _, d := range res.Deliverables {
			d := d
			parentOut <- types.EngineEvent{
				Type:        types.EngineEventDeliverable,
				AgentName:   in.Description,
				Deliverable: &d,
			}
		}
	}

	payload := map[string]any{
		"status":       res.Status,
		"steps":        res.Steps,
		"deliverables": res.Deliverables,
	}
	metadata := map[string]any{
		"render_hint":   "orchestrate",
		"step_count":    len(res.Steps),
		"status":        res.Status,
		"duration_ms":   time.Since(startTime).Milliseconds(),
		"deliverables":  res.Deliverables,
	}
	isError := res.Status == exec.StatusFailed
	return jsonResult(payload, metadata, isError), nil
}

// runPlanner spawns the Planner sub-agent up to MaxPlannerAttempts times,
// validating the returned plan each time. Returns the first valid plan, or
// nil + the last error.
func (t *OrchestrateTool) runPlanner(
	ctx context.Context,
	intent string,
	available []string,
	listings []agent.PlannerListing,
	parentSessionID string,
	parentOut chan<- types.EngineEvent,
	allowedSet map[string]bool,
) (*exec.Plan, error) {
	prompt := buildPlannerPrompt(intent, available, listings)
	var lastErr error
	var lastSummary string

	for attempt := 1; attempt <= MaxPlannerAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		retryPrompt := prompt
		if attempt > 1 {
			retryPrompt = appendRetryHint(prompt, lastErr, lastSummary)
		}

		cfg := &agent.SpawnConfig{
			Prompt:          retryPrompt,
			AgentType:       tool.AgentTypeSync,
			SubagentType:    PlannerSubagentType,
			Description:     "orchestrate planner",
			Name:            fmt.Sprintf("planner-%d", attempt),
			ParentSessionID: parentSessionID,
			ParentOut:       parentOut,
			Timeout:         2 * time.Minute,
		}

		spawnRes, err := t.spawner.SpawnSync(ctx, cfg)
		if err != nil {
			lastErr = fmt.Errorf("spawn planner: %w", err)
			t.logger.Warn("planner attempt failed (spawn error)",
				zap.Int("attempt", attempt), zap.Error(err))
			continue
		}
		if spawnRes == nil {
			lastErr = fmt.Errorf("planner returned nil result")
			continue
		}

		plan, parseErr := exec.ParsePlan(spawnRes.Output)
		if parseErr != nil {
			lastErr = parseErr
			lastSummary = spawnRes.Summary
			t.logger.Warn("planner attempt failed (parse error)",
				zap.Int("attempt", attempt), zap.Error(parseErr))
			continue
		}

		if validateErr := plan.Validate(allowedSet); validateErr != nil {
			lastErr = validateErr
			lastSummary = spawnRes.Summary
			t.logger.Warn("planner attempt failed (validation)",
				zap.Int("attempt", attempt), zap.Error(validateErr))
			continue
		}

		// Success.
		return plan, nil
	}

	return nil, lastErr
}

// resolveAgents merges caller overrides with the runtime roster, dedupes,
// and sorts for prompt-cache stability.
func (t *OrchestrateTool) resolveAgents(override []string) []string {
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == PlannerSubagentType {
			return
		}
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	for _, n := range override {
		add(n)
	}
	if t.roster != nil {
		for _, n := range t.roster.AvailableSubagentTypes() {
			add(n)
		}
	}
	sort.Strings(out)
	return out
}

// --- helpers ---

func toSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true}
}

func jsonResult(payload map[string]any, metadata map[string]any, isError bool) *types.ToolResult {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return errResult("marshal orchestrate result: " + err.Error())
	}
	return &types.ToolResult{
		Content:  string(body),
		IsError:  isError,
		Metadata: metadata,
	}
}

// buildPlannerPrompt constructs the Planner sub-agent's user message.
// When listings is non-empty, each agent's description / limitations /
// example tasks / cost tier are included so the Planner can make
// informed routing decisions. When listings is nil (tests / coordinators
// without a rich registry), falls back to the name-only format.
func buildPlannerPrompt(intent string, available []string, listings []agent.PlannerListing) string {
	var b strings.Builder
	b.WriteString("# 任务\n\n请把下面的用户意图拆解成可执行的计划 JSON。\n\n")
	b.WriteString("# 用户意图\n\n")
	b.WriteString(strings.TrimSpace(intent))
	b.WriteString("\n\n# 可用搭档\n\n")

	// Build a lookup from name → listing for O(1) augmentation below.
	listingByName := make(map[string]agent.PlannerListing, len(listings))
	for _, l := range listings {
		listingByName[l.Name] = l
	}

	if len(available) == 0 {
		b.WriteString("- worker（默认执行型）\n")
	} else {
		for _, name := range available {
			if l, ok := listingByName[name]; ok {
				// Rich format: give the Planner the metadata it needs to
				// choose correctly. Only include non-empty sections so
				// lean definitions don't produce noisy blank lines.
				label := name
				if l.DisplayName != "" {
					label = fmt.Sprintf("%s（%s）", name, l.DisplayName)
				}
				costStr := ""
				switch l.CostTier {
				case agent.CostCheap:
					costStr = " [cheap]"
				case agent.CostExpensive:
					costStr = " [expensive]"
				}
				fmt.Fprintf(&b, "## %s%s\n", label, costStr)
				if l.Description != "" {
					fmt.Fprintf(&b, "描述：%s\n", l.Description)
				}
				if len(l.Skills) > 0 {
					fmt.Fprintf(&b, "技能标签：%s\n", strings.Join(l.Skills, " / "))
				}
				if len(l.Limitations) > 0 {
					b.WriteString("不适合：")
					for i, lim := range l.Limitations {
						if i > 0 {
							b.WriteString("；")
						}
						b.WriteString(lim)
					}
					b.WriteString("\n")
				}
				if len(l.ExampleTasks) > 0 {
					b.WriteString("示例任务：")
					show := l.ExampleTasks
					if len(show) > 2 {
						show = show[:2] // keep prompt short
					}
					for i, ex := range show {
						if i > 0 {
							b.WriteString("；")
						}
						b.WriteString(ex)
					}
					b.WriteString("\n")
				}
				b.WriteString("\n")
			} else {
				// Fallback: name only (built-in profiles without a registry entry).
				fmt.Fprintf(&b, "- %s\n", name)
			}
		}
	}
	b.WriteString("严格按 system prompt 中的 JSON Schema 返回，不要多写正文。")
	return b.String()
}

func appendRetryHint(base string, lastErr error, lastSummary string) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n# 上次输出存在问题\n")
	if lastErr != nil {
		fmt.Fprintf(&b, "- 错误：%s\n", lastErr.Error())
	}
	if lastSummary != "" {
		fmt.Fprintf(&b, "- 上次摘要：%s\n", lastSummary)
	}
	b.WriteString("请严格按 schema 重新输出 JSON，不要重复同样的错误。\n")
	return b.String()
}

const orchestrateDescription = `把多步意图拆成计划，按 sub-agent DAG 并行执行。

仅当用户请求需要多个 sub-agent 步骤、且步骤间有明确顺序或数据依赖时（如"调研 → 分析 → 写报告"）才用 Orchestrate。单步一次搞定的任务用 Agent 工具——开销更小，控制更直接。

工作流程：
- 传入一行自然语言 ` + "`intent`" + ` 描述总体目标。
- 内部 Planner 把 intent 转成 plan JSON（带依赖的步骤列表）。
- 没有未完成依赖的步骤会并行执行。
- 每步的 <summary> 自动注入到它依赖步骤的上下文里。
- 工具返回 { status, steps, deliverables }。

失败模式：
- Planner 连续 3 次产不出合法 plan 时，返回 { status: "plan_failed", degraded: true }。看到 degraded:true 时，请改成自己手动串行调 Agent。
- 单步失败会级联标记下游步骤为 "skipped"；只要有一步成功，总 status 就是 "partial_completed"。

注意：
- 单个 plan 最多 10 步。
- Orchestrate 内部的 sub-agent 不能递归调 Orchestrate 或 Agent。
- ` + "`intent`" + ` 必填；` + "`description`" + ` 和 ` + "`available_agents`" + ` 可选。`
