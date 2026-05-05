package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/pkg/types"
)

// RenderExpectedOutputs builds the `<expected-outputs>` preamble block
// that the framework prepends to an L3 task prompt when the dispatcher
// declared a deliverable contract. Returns "" when the contract is empty
// so callers can compose unconditionally.
//
// Doc §6 (mode A) + §3 (mechanism M1): the LLM sees the contract verbatim
// in its task prompt. Without it, the LLM has only the artifacts section
// to go on — useful but not specific to "what THIS task must produce".
//
// Format intentionally lists role + type + size + schema/criteria first
// (the binding fields the LLM needs to satisfy) and pushes optional /
// informational fields second.
func RenderExpectedOutputs(outs []types.ExpectedOutput) string {
	if len(outs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<expected-outputs>\n")
	b.WriteString("本任务必须通过 ArtifactWrite 写入以下产物，并在结束时调用 SubmitTaskResult 声明 ID 列表。每条 role 一对一对应一份 artifact。\n\n")

	for _, o := range outs {
		req := "(可选)"
		if o.Required {
			req = "(必须)"
		}
		fmt.Fprintf(&b, "- role: %s %s\n", o.Role, req)
		if o.Type != "" {
			fmt.Fprintf(&b, "  type: %s\n", o.Type)
		}
		if o.MIMEType != "" {
			fmt.Fprintf(&b, "  mime_type: %s\n", o.MIMEType)
		}
		if o.MinSizeBytes > 0 {
			fmt.Fprintf(&b, "  min_size_bytes: %d\n", o.MinSizeBytes)
		}
		if len(o.Schema) > 0 {
			fmt.Fprintf(&b, "  schema: %s\n", string(o.Schema))
		}
		if o.AcceptanceCriteria != "" {
			fmt.Fprintf(&b, "  acceptance: %s\n", o.AcceptanceCriteria)
		}
	}
	b.WriteString("\n硬要求：\n")
	b.WriteString("1. 必须用 ArtifactWrite 把每份产出写进 store；不能只在文本里给出。\n")
	b.WriteString("2. 全部产出写完后，必须调用 SubmitTaskResult 一次，提交 {role, artifact_id} 列表 + ≤200 字总结。\n")
	b.WriteString("3. SubmitTaskResult 之外的最终文本不要重复正文 —— 数据已在 artifact，summary 只写过程要点。\n")
	b.WriteString("</expected-outputs>")
	return b.String()
}

// RenderSubAgentContract builds the `<sub-agent-contract>` preamble block
// that the framework prepends to a TierSubAgent's task prompt.
//
// Where this differs from RenderExpectedOutputs:
//   - ExpectedOutputs is per-SPAWN (the dispatcher's "this task needs X").
//   - SubAgentContract is per-DEFINITION (the registry's "this agent
//     always produces THIS shape, has THESE limitations, can't do THESE
//     things"). Stable across calls to the same agent — derived from
//     AgentDefinition.OutputSchema / Skills / Limitations.
//
// Both blocks coexist in the L3 prompt: SubAgentContract first (the
// agent's permanent identity / contract), ExpectedOutputs second (the
// task-specific delta on top).
//
// Returns "" for nil definitions or non-SubAgent tiers — callers can
// compose unconditionally without a tier check.
func RenderSubAgentContract(def *AgentDefinition) string {
	if def == nil || def.EffectiveTier() != TierSubAgent {
		return ""
	}

	// Keep this block FOCUSED on what's L3-specific. Anything also covered
	// by `<artifacts-guidance>` (ArtifactWrite mechanics, SubmitTaskResult
	// usage, <summary> output rules) lives there — repeating it here only
	// inflates token count without adding signal.
	var b strings.Builder
	b.WriteString("<sub-agent-contract>\n")
	b.WriteString("你是 L3 sub-agent（叶子执行者）。L3 独有的两条硬规则：\n\n")
	b.WriteString("1. **不下派**：不能调用 Task / Specialists / Orchestrate 把活给其他 agent；遇到自己做不了的，走第 2 条。\n")
	b.WriteString("2. **EscalateToPlanner 是合法出口**：任务确实做不了时（缺输入、超能力、约束矛盾），调 EscalateToPlanner({reason, suggested_next_steps}) 而不是硬写一个差产物。\n\n")

	if len(def.Skills) > 0 {
		b.WriteString("能力标签：")
		b.WriteString(strings.Join(def.Skills, " / "))
		b.WriteString("\n\n")
	}

	if len(def.OutputSchema) > 0 {
		schemaJSON, err := json.MarshalIndent(def.OutputSchema, "", "  ")
		if err == nil {
			b.WriteString("产出契约 (output_schema)：\n")
			b.WriteString("```json\n")
			b.Write(schemaJSON)
			b.WriteString("\n```\n\n")
		}
	}

	if len(def.Limitations) > 0 {
		b.WriteString("不做事项（命中即 EscalateToPlanner）：\n")
		for _, l := range def.Limitations {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		b.WriteString("\n")
	}

	b.WriteString("</sub-agent-contract>")
	return b.String()
}
