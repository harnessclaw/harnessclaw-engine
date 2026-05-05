package submittool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// EscalateToolName is the LLM-facing name for the escalation tool.
//
// Why a dedicated tool rather than a SubmitTaskResult status field:
// escalation is a different terminal class. SubmitTaskResult means "task
// done, here are the deliverables"; EscalateToPlanner means "task
// undoable as scoped, please re-plan." Conflating them blurs the loop
// driver's exit logic and pollutes the planner-facing surface area.
const EscalateToolName = "EscalateToPlanner"

// EscalateMetadataRenderHint is the unique signal Execute writes to
// ToolResult.Metadata "render_hint" — the L3 driver reads this to detect
// "this turn requested an escalation" and exits the loop with
// NeedsPlanning=true. String compare keeps the detection O(1).
const EscalateMetadataRenderHint = "task_escalation"

// EscalateMaxReasonChars caps the reason field. Long enough for a
// meaningful explanation, short enough to keep the planner's input
// digestible when many sub-agents escalate at once.
const EscalateMaxReasonChars = 400

// EscalateMaxStepsChars caps the suggested_next_steps field. Same
// reasoning as EscalateMaxReasonChars — guidance, not a manifesto.
const EscalateMaxStepsChars = 400

// escalation is the parsed input.
type escalation struct {
	Reason             string `json:"reason"`
	SuggestedNextSteps string `json:"suggested_next_steps,omitempty"`
}

// EscalateTool is the L3 needs-planning escape hatch.
//
// Purpose: when a sub-agent realizes it CAN'T complete the task as
// scoped (missing capability, ambiguous instruction, contradictory
// constraint), it calls this tool to hand control back to the planner
// (L2) instead of guessing or returning garbage.
//
// Doc §1 mode: the user's design point #2 — "Sub-agents 之间不能互相调用".
// When stuck, the L3 returns needs_planning to L2; L2 decides whether to
// retry, re-decompose, or fail upward. This tool is the protocol for
// that handoff.
type EscalateTool struct {
	tool.BaseTool
}

// NewEscalate returns a registered EscalateToolName instance.
func NewEscalate() *EscalateTool { return &EscalateTool{} }

func (*EscalateTool) Name() string            { return EscalateToolName }
func (*EscalateTool) Description() string     { return escalateDescription }
func (*EscalateTool) IsReadOnly() bool                  { return false }
func (*EscalateTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (*EscalateTool) IsEnabled() bool         { return true }
func (*EscalateTool) IsConcurrencySafe() bool { return false } // terminal action; serial

// InputSchema enforces the minimal escalation contract:
//   - reason: required, ≤ EscalateMaxReasonChars
//   - suggested_next_steps: optional, ≤ EscalateMaxStepsChars
func (*EscalateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("一段（≤%d 字）说清楚为什么任务在当前作用域内做不了。要具体——'任务要 Q4 销量数据但 <available-artifacts> 里没源文件'比'我信息不够'有用得多。", EscalateMaxReasonChars),
				"maxLength":   EscalateMaxReasonChars,
			},
			"suggested_next_steps": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("可选（≤%d 字）。你认为调度方下一步该怎么处理——拆任务、补输入、换 sub-agent、问用户、还是中止。", EscalateMaxStepsChars),
				"maxLength":   EscalateMaxStepsChars,
			},
		},
		"required": []string{"reason"},
	}
}

func (*EscalateTool) ValidateInput(raw json.RawMessage) error {
	var e escalation
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(e.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	if utf8Len(e.Reason) > EscalateMaxReasonChars {
		return fmt.Errorf("reason too long: %d chars (max %d)", utf8Len(e.Reason), EscalateMaxReasonChars)
	}
	if utf8Len(e.SuggestedNextSteps) > EscalateMaxStepsChars {
		return fmt.Errorf("suggested_next_steps too long: %d chars (max %d)", utf8Len(e.SuggestedNextSteps), EscalateMaxStepsChars)
	}
	return nil
}

// Execute records the escalation request. The driver loop reads the
// render_hint to detect this turn requested an escalation and exits
// with NeedsPlanning=true.
//
// IsError stays false: from the L3's perspective, escalating cleanly
// is a SUCCESS — it correctly identified an impasse and reported it.
// The "did the task succeed" signal lives in SpawnResult.NeedsPlanning,
// not in this tool's IsError.
func (*EscalateTool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var e escalation
	if err := json.Unmarshal(raw, &e); err != nil {
		// Schema layer should have caught this; defensive only.
		return &types.ToolResult{
			Content: "Escalation rejected: " + err.Error(),
			IsError: true,
		}, nil
	}

	body, _ := json.Marshal(struct {
		Status             string `json:"status"`
		Reason             string `json:"reason"`
		SuggestedNextSteps string `json:"suggested_next_steps,omitempty"`
	}{
		Status:             "needs_planning",
		Reason:             e.Reason,
		SuggestedNextSteps: e.SuggestedNextSteps,
	})

	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint":          EscalateMetadataRenderHint,
			"escalation_reason":    e.Reason,
			"suggested_next_steps": e.SuggestedNextSteps,
		},
	}, nil
}

const escalateDescription = `把任务退回给调度方——当你在当前作用域内确实无法完成时调用本工具。

何时调用：
- 缺关键输入而你无法自己产出（例如：任务要 Q4 销量但 <available-artifacts> 里没源文件）。
- 指令本身就有歧义，凭上下文也没法消解。
- 约束之间冲突（例如：要求"翻译成法语"，但源文已经是法语）。
- 任务需要你不具备的能力（例如：让 writer 去编译代码）。

不要用这个工具来：
- 偷懒——先实打实试一次，确实不行再 escalate。
- 跳过你本可通过仔细读 prompt 解决的歧义。
- 因为某次工具调用失败就放弃——先换思路重试一次。

输入：
- reason（必填，≤400 字）：任务在当前作用域为什么做不了。要具体。
- suggested_next_steps（可选，≤400 字）：你认为调度方下一步该做什么——拆任务、补输入、换 sub-agent、问用户、还是中止。

效果：
框架把本工具视为终止动作。你的 loop 结束；上层收到 status=needs_planning，由它决定是否换个范围重试。调本工具不算失败——这是对"不可能任务"的正确反应。

同一个任务里，本工具与 SubmitTaskResult 互斥，只能调其中之一。`
