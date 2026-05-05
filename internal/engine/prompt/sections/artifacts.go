package sections

import "harnessclaw-go/internal/engine/prompt"

// ArtifactsSection teaches workers WHEN to use ArtifactWrite / ArtifactRead.
// The tools' own input schemas describe HOW to call them; this section is
// the policy layer the Tools section can't carry — pasting the same text
// into both tools' descriptions would waste budget and drift over time.
//
// Priority 25 sits between tools(20) and env(30) so it's read right after
// the tool roster, while the agent still has "which tools exist" in mind.
//
// Cacheable: the guidance is profile-static (no context interpolation), so
// the section is reusable across turns and across sessions of the same
// profile — keeping it cacheable means the prefix stays stable for prompt
// caching.
type ArtifactsSection struct{}

// NewArtifactsSection constructs the section.
func NewArtifactsSection() *ArtifactsSection {
	return &ArtifactsSection{}
}

func (s *ArtifactsSection) Name() string    { return "artifacts" }
func (s *ArtifactsSection) Priority() int   { return 25 }
func (s *ArtifactsSection) Cacheable() bool { return true }
func (s *ArtifactsSection) MinTokens() int  { return 60 }

// Render returns the static guidance — no context fields are interpolated,
// so the budget argument is effectively ignored beyond MinTokens gating.
func (s *ArtifactsSection) Render(_ *prompt.PromptContext, _ int) (string, error) {
	return artifactsGuidance, nil
}

// artifactsGuidance — single source of truth, edit here.
//
// Trimmed for token economy (P0 优化, 2026-05-04): per-tool "when to write
// / when to read" guidance lives in ArtifactWrite / ArtifactRead descriptions
// already; repeating it here only inflates the prompt without adding signal.
//
// What stays is the cross-cutting policy that no single tool description
// owns:
//   1. Mental model — artifacts as references, not values.
//   2. Hard rules — invariants the LLM must never violate
//      (e.g. don't invent IDs).
//   3. Final-text contract — what the assistant's last message must look
//      like (summary + SubmitTaskResult, never the artifact content).
//
// Keep it terse. Anything detailed enough to need an example block belongs
// in the relevant tool's description.
const artifactsGuidance = `# Artifact 使用规范

Artifact 是跨 agent 共享数据的载体——把"按值传递"改成"按引用传递"。
给下游一个 ` + "`artifact_id`" + ` 让它自己去 store 取，**不要把整段内容塞回 prompt**。

何时调 ArtifactWrite / ArtifactRead 详见各工具描述。

## 硬规则

- ` + "`artifact_id`" + ` **由 ArtifactWrite 返回，绝不能自己编**——只能从 ArtifactWrite 的工具返回值或上游消息里**逐字复制**。
- 真实 ID = ` + "`art_`" + ` + **正好 24 个 16 进制字符**（如 ` + "`art_2a7f0e8b4c9d11ef0a36e613`" + `）。**带破折号的 UUID（` + "`xxxxxxxx-xxxx-...`" + `）或 32 hex 长串 100% 是幻觉**，校验立即拒绝。
- **不知道 ID 时**：sub-agent 调 ` + "`EscalateToPlanner`" + ` 上报"缺少 artifact 引用"；L2 在 summary 里说明并请上游补——**禁止猜**。
- 收到的 artifact 不原样转发回 prompt——下游想要自己去 read。
- 修改同一份逻辑数据 → 用 ` + "`parent_artifact_id`" + ` 产生新 version，不要原地覆盖。

## 最终输出契约

完成任务时只输出两样：
1. 一个 ` + "`<summary>`" + ` 块（≤200 字），含过程要点 + artifact_id 引用。
2. 同时调用 ` + "`SubmitTaskResult`" + ` 工具（任务带 ` + "`<expected-outputs>`" + ` 契约或你是 sub-agent 时**必调**）。

**严禁**：在 summary 或 assistant 正文里复制 artifact 内容、粘贴 preview、用 markdown 把数据"展示"出来——这些必须进 ArtifactWrite。`
