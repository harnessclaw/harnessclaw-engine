package agent

import (
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
