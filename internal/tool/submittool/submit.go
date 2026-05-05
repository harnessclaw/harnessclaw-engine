// Package submittool implements SubmitTaskResult — the L3-facing
// declaration that "I'm done, here are my deliverables".
//
// Doc §3 mechanism M3 (structured output) + M4 (server-side validation)
// + part of M5 (final-text shape). Why this is a SEPARATE tool from
// ArtifactWrite, see chat history: write is a per-data-blob action;
// submit is a per-task terminal action. Conflating them weakens schema
// enforcement and blurs loop-termination semantics.
//
// Validation outcomes:
//
//   - Schema violations are caught by ValidateInput (LLM provider rejects
//     the malformed call before it reaches Execute).
//   - Existence / lineage / size / type / role failures land as ToolResult
//     IsError=true with a precise reason; the LLM sees the reason in the
//     next turn and re-tries. The loop stays running.
//   - Validation success returns IsError=false plus metadata that the
//     loop's tool_end handler reads to flag "submission complete" — the
//     loop then accepts end_turn as terminal.
package submittool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the LLM-facing name. PascalCase to match OpenAI's
// `^[a-zA-Z0-9_-]+$` constraint and the rest of the tool palette.
const ToolName = "SubmitTaskResult"

// MetadataRenderHint is the value Execute writes to ToolResult.Metadata
// "render_hint" on success. The runSubAgentLoop uses it as the unique
// signal that submission passed validation — a string compare keeps the
// detection path O(1) and decoupled from this package's internals.
const MetadataRenderHint = "task_submission"

// MetadataKeyAccepted is the metadata key that records whether validation
// passed. Both branches (accepted / rejected) emit a tool_end so the loop
// can also count REJECTED submissions toward the retry cap (M2).
const MetadataKeyAccepted = "submission_accepted"

// MetadataKeyArtifacts carries the validated []types.ArtifactRef so the
// loop can attach them to the SpawnResult and the parent gets them
// without re-querying the store.
const MetadataKeyArtifacts = "submitted_artifacts"

// MaxSummaryChars caps the LLM's summary text. Doc §1 failure #4
// (double-write leak) is addressed by keeping summary short — there's
// just no room for the LLM to paste back the full report.
const MaxSummaryChars = 200

// submission is the parsed input.
type submission struct {
	Artifacts []submittedArtifact `json:"artifacts"`
	Summary   string              `json:"summary"`
	// Result is the structured payload validated against the agent's
	// OutputSchema (when set). Optional for legacy / no-schema agents;
	// required when TaskContract.OutputSchema is non-empty.
	Result map[string]any `json:"result,omitempty"`
}

// submittedArtifact is one entry in the deliverable list.
type submittedArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Role       string `json:"role"`
}

// Tool is the L3 task-submission tool.
type Tool struct {
	tool.BaseTool
}

// New returns the registered tool instance.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string             { return ToolName }
func (*Tool) Description() string      { return description }
func (*Tool) IsReadOnly() bool                  { return false }
func (*Tool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*Tool) IsEnabled() bool          { return true }
func (*Tool) IsConcurrencySafe() bool  { return false } // terminal action; serial

// InputSchema enforces the M3 hard contract:
//   - artifacts is a non-empty array of {artifact_id, role}
//   - summary is required and capped at MaxSummaryChars
//
// minItems is set to 1 unconditionally — a task that needs zero
// deliverables shouldn't be calling this tool at all (the loop accepts
// plain end_turn there). When it IS called, the LLM is committing to at
// least one ID. Per-required-role enforcement happens at runtime in
// Execute since the schema is registered statically.
func (*Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"artifacts": map[string]any{
				"type":        "array",
				"description": "{artifact_id, role} 列表，每个产出对应一项。artifact_id 必须是本任务内由 ArtifactWrite 返回的——不能编造，也不能复用其他任务的 ID。",
				"minItems":    1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "ArtifactWrite 返回的 ID，格式 art_<24 位十六进制>。",
						},
						"role": map[string]any{
							"type":        "string",
							"description": "对应 <expected-outputs> 中的 role 名；表明此产出在父任务契约里扮演的角色。",
						},
					},
					"required": []string{"artifact_id", "role"},
				},
			},
			"summary": map[string]any{
				"type":        "string",
				"description": fmt.Sprintf("过程要点 + ID 引用，≤%d 字。绝不写正文内容——数据已在 artifact 里。示例：'Q4 调研完成，5 条核心结论 (art_xxx)；月度对比表 (art_yyy)。'", MaxSummaryChars),
				"maxLength":   MaxSummaryChars,
			},
			"result": map[string]any{
				"type":        "object",
				"description": "结构化产出，必须满足 <sub-agent-contract> 块中声明的 output_schema。声明了 output_schema 的 sub-agent 必填；老式 agent 可省。字段名、enum 取值、最小值都会服务端校验，被拒会指出哪个字段不合格。",
			},
		},
		"required": []string{"artifacts", "summary"},
	}
}

// ValidateInput catches malformed JSON and obviously-broken IDs before
// the call reaches the validation pipeline, giving the LLM a faster
// feedback loop on shape problems.
func (*Tool) ValidateInput(raw json.RawMessage) error {
	var s submission
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if len(s.Artifacts) == 0 {
		return fmt.Errorf("artifacts list must contain at least one entry")
	}
	if strings.TrimSpace(s.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if utf8Len(s.Summary) > MaxSummaryChars {
		return fmt.Errorf("summary too long: %d chars (max %d)", utf8Len(s.Summary), MaxSummaryChars)
	}
	for i, a := range s.Artifacts {
		if !artifact.IsValidID(a.ArtifactID) {
			return fmt.Errorf("artifacts[%d].artifact_id %q is malformed (expected art_<24 hex>)", i, a.ArtifactID)
		}
		if strings.TrimSpace(a.Role) == "" {
			return fmt.Errorf("artifacts[%d].role is required", i)
		}
	}
	return nil
}

// Execute runs the M4 server-side validation pipeline. Any failure
// produces a tool_error with a SPECIFIC, actionable reason — the LLM
// reads it in the next turn and corrects, rather than retrying blind.
func (*Tool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var s submission
	if err := json.Unmarshal(raw, &s); err != nil {
		return rejected("invalid input: " + err.Error())
	}

	storeAny, ok := tool.GetArtifactStoreValue(ctx)
	if !ok {
		return rejected("artifact store not configured (engine misconfiguration)")
	}
	store, ok := storeAny.(artifact.Store)
	if !ok {
		return rejected("artifact store has unexpected type (engine misconfiguration)")
	}
	contract, _ := tool.GetTaskContract(ctx)

	// 1. Validate each submitted ID against the store + contract.
	validated := make([]types.ArtifactRef, 0, len(s.Artifacts))
	var failures []string
	for i, sub := range s.Artifacts {
		if reason := validateOne(ctx, store, contract, sub); reason != "" {
			failures = append(failures, fmt.Sprintf("artifacts[%d] (id=%s, role=%s): %s",
				i, sub.ArtifactID, sub.Role, reason))
			continue
		}
		// Re-fetch as the canonical record so the metadata we ship to
		// the parent is the store's truth, not the LLM's claim.
		a, err := store.Get(ctx, sub.ArtifactID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("artifacts[%d]: store.Get failed: %v", i, err))
			continue
		}
		ref := types.ArtifactRef{
			ArtifactID:  a.ID,
			Name:        a.Name,
			Type:        string(a.Type),
			MIMEType:    a.MIMEType,
			SizeBytes:   a.Size,
			Description: a.Description,
			PreviewText: a.Preview,
			URI:         a.URI,
			Role:        sub.Role,
		}
		validated = append(validated, ref)
	}

	// 2. Enforce required-output coverage from the contract.
	if msg := checkRequiredCovered(contract, s.Artifacts); msg != "" {
		failures = append(failures, msg)
	}

	// 3. Validate the structured `result` payload against the agent's
	//    declared OutputSchema (P0-1). When the contract has a schema and
	//    the LLM omitted `result` or got it wrong, the failure list lands
	//    here with field-level reasons the next turn can act on.
	if len(contract.OutputSchema) > 0 {
		if schemaFails := validateAgainstSchema(contract.OutputSchema, s.Result); len(schemaFails) > 0 {
			for _, f := range schemaFails {
				failures = append(failures, "result."+f)
			}
		}
	}

	if len(failures) > 0 {
		return rejected(formatFailures(failures))
	}

	// Success path — emit a structured payload + a clear status string.
	body, _ := json.Marshal(struct {
		Status    string              `json:"status"`
		Summary   string              `json:"summary"`
		Artifacts []types.ArtifactRef `json:"artifacts"`
		Result    map[string]any      `json:"result,omitempty"`
	}{
		Status:    "accepted",
		Summary:   s.Summary,
		Artifacts: validated,
		Result:    s.Result,
	})
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint":          MetadataRenderHint,
			MetadataKeyAccepted:    true,
			MetadataKeyArtifacts:   validated,
			"summary":              s.Summary,
		},
	}, nil
}

// validateOne returns "" when the submission entry passes every M4 check;
// otherwise a sentence explaining which check failed.
func validateOne(
	ctx context.Context,
	store artifact.Store,
	contract tool.TaskContract,
	sub submittedArtifact,
) string {
	a, err := store.Get(ctx, sub.ArtifactID)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			return "artifact not found in store (was it actually written? IDs are not invent-able)"
		}
		return "store error: " + err.Error()
	}

	// 1. Producer/task lineage — guards failure #8 (claiming foreign artifact).
	if contract.TaskID != "" && a.Producer.TaskID != contract.TaskID {
		return fmt.Sprintf("artifact's producer.task_id (%q) does not match this task (%q); "+
			"you may only submit artifacts written DURING this task",
			a.Producer.TaskID, contract.TaskID)
	}

	// 2. Temporal — guards "claim a pre-existing artifact".
	if !contract.TaskStartedAt.IsZero() && a.CreatedAt.Before(contract.TaskStartedAt) {
		return fmt.Sprintf("artifact created_at (%s) precedes task start (%s); cannot submit historical artifacts",
			a.CreatedAt.Format("15:04:05"), contract.TaskStartedAt.Format("15:04:05"))
	}

	// 3. Size — guards failure #3 (placeholder/empty).
	if a.Size <= 0 {
		return "artifact is empty (size 0); placeholder writes are not accepted"
	}

	// 4. Role <-> contract match (when the dispatcher provided one).
	if len(contract.ExpectedOutputs) > 0 {
		expected, ok := findExpected(contract, sub.Role)
		if !ok {
			roles := expectedRoles(contract)
			return fmt.Sprintf("role %q is not in the contract; valid roles: %v", sub.Role, roles)
		}
		if expected.Type != "" && string(a.Type) != expected.Type {
			return fmt.Sprintf("artifact type %q does not match expected %q for role %q",
				a.Type, expected.Type, sub.Role)
		}
		if expected.MIMEType != "" && a.MIMEType != expected.MIMEType {
			return fmt.Sprintf("artifact mime_type %q does not match expected %q for role %q",
				a.MIMEType, expected.MIMEType, sub.Role)
		}
		minSize := expected.MinSizeBytes
		if minSize == 0 {
			minSize = 1
		}
		if a.Size < minSize {
			return fmt.Sprintf("artifact size %d < min_size_bytes %d for role %q",
				a.Size, minSize, sub.Role)
		}
	}

	return ""
}

// checkRequiredCovered returns "" when every Required=true entry in the
// contract has at least one corresponding submitted artifact, otherwise
// a description of the gap.
func checkRequiredCovered(contract tool.TaskContract, submitted []submittedArtifact) string {
	if len(contract.ExpectedOutputs) == 0 {
		return ""
	}
	delivered := make(map[string]bool, len(submitted))
	for _, s := range submitted {
		delivered[s.Role] = true
	}
	var missing []string
	for _, want := range contract.ExpectedOutputs {
		if want.Required && !delivered[want.Role] {
			missing = append(missing, want.Role)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("required output(s) not submitted: %v — write the missing artifact(s) and call SubmitTaskResult again",
		missing)
}

// findExpected returns the contract entry matching role, false when no
// match. Linear scan — contracts are tiny, hashing is overkill.
func findExpected(contract tool.TaskContract, role string) (types.ExpectedOutput, bool) {
	for _, e := range contract.ExpectedOutputs {
		if e.Role == role {
			return e, true
		}
	}
	return types.ExpectedOutput{}, false
}

func expectedRoles(contract tool.TaskContract) []string {
	out := make([]string, 0, len(contract.ExpectedOutputs))
	for _, e := range contract.ExpectedOutputs {
		out = append(out, e.Role)
	}
	return out
}

// rejected wraps a failure reason in the standard rejected-submission
// shape: tool_error + render_hint that the loop reads to count toward
// the retry cap and surface to telemetry.
func rejected(reason string) (*types.ToolResult, error) {
	return &types.ToolResult{
		Content: "Submission rejected: " + reason,
		IsError: true,
		Metadata: map[string]any{
			"render_hint":       MetadataRenderHint,
			MetadataKeyAccepted: false,
			"reason":            reason,
		},
	}, nil
}

// formatFailures composes the multi-line rejection body. Every line
// starts with the offending index/id so the LLM can match the message
// to its input on next turn.
func formatFailures(failures []string) string {
	var b strings.Builder
	b.WriteString("Submission failed validation. Fix and call SubmitTaskResult again:\n")
	for _, f := range failures {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// utf8Len counts runes (not bytes). Avoids spurious "too long" errors
// when summary contains Chinese or other multi-byte characters that would
// otherwise blow byte-level limits.
func utf8Len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

const description = `通过提交已产出的 artifact 声明任务完成。

何时调用本工具：
- 你是 sub-agent（prompt 中含 <sub-agent-contract> 块）——必须调用本工具 或 EscalateToPlanner 之一才能终止。仅 end_turn 会被拒绝，框架会最多催促 3 次后判任务失败。
- 你不是 sub-agent 但任务带 <expected-outputs> 块——写完每个必交 artifact 后，必须调用一次。
- 你不是 sub-agent 且没有 <expected-outputs> 块——请勿调用本工具，直接输出 <summary> 后结束即可。

若 <sub-agent-contract> 与 <expected-outputs> 同时存在，必须双重满足：<expected-outputs> 中所有 required=true 的 role 都得出现在 artifacts 列表里。

输入字段：
- artifacts：{artifact_id, role} 列表，每个产出对应一项。artifact_id 必须是本任务内由 ArtifactWrite 调用返回的（框架会做服务端校验）。role 在有 <expected-outputs> 时必须匹配其中条目；没有时则用一个描述性名称（如 "draft_email"、"summary_report"）。
- summary：≤200 字。过程要点 + ID 引用。绝不能在这里粘贴 artifact 正文——数据已在 artifact 里。
- result：当 <sub-agent-contract> 中声明了 output_schema 时必填。结构化字段必须满足该 schema（必填字段、enum 取值、最小值都会服务端校验）。

服务端校验：
框架对每个 artifact_id 做如下检查：
  - 在 store 中存在
  - 由本次任务产生（producer.task_id 匹配）
  - 创建时间晚于任务开始时间（防"穿越"）
  - content 非空（不接受占位符）
  - 当存在 <expected-outputs> 契约时，匹配其声明的 type / mime_type / min_size_bytes / role

任一项失败时，本工具会返回带具体原因的错误。重写有问题的 artifact 后再次调用 SubmitTaskResult。连续 3 次被拒，任务判失败。

如果任务在当前作用域内确实无法完成（缺关键输入、约束冲突、能力差距），请改调 EscalateToPlanner——那是"我做不到"的合法出口，不算失败。`
