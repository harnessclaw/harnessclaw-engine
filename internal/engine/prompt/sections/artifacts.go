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
func (s *ArtifactsSection) MinTokens() int  { return 80 }

// Render returns the static guidance — no context fields are interpolated,
// so the budget argument is effectively ignored beyond MinTokens gating.
func (s *ArtifactsSection) Render(_ *prompt.PromptContext, _ int) (string, error) {
	return artifactsGuidance, nil
}

// artifactsGuidance — single source of truth, edit here.
//
// Distillation of the artifact design doc. Three things matter for the
// LLM at run time, in this order:
//   (1) The mental model: artifacts replace pasting-by-value with
//       passing-by-reference. Every section below derives from this.
//   (2) The decision rules: when to write, when to read, in which mode.
//   (3) The hard prohibitions: anti-patterns that silently break lineage,
//       cost, or correctness if the LLM is left to its own devices.
//
// We keep the prose short on purpose — workers need a decision flowchart,
// not a concept lecture. Anything that isn't directly actionable in a
// tool-call decision belongs in the design doc, not the prompt.
const artifactsGuidance = `# Artifact 使用规范

Artifact 是跨 agent 共享数据的载体——把"按值传递"改成"按引用传递"。
你给下游 agent 一个 ` + "`artifact_id`" + `，对方拿 ID 自己去 store 取，**不要把整段内容塞回 prompt**。
每多塞一层，token 翻倍、信息会被复述损耗、调试时无血缘可查。

## 何时 ArtifactWrite

写：
- 产出**会被另一个 agent 消费**的数据（报告、表格、调研结果、代码片段）
- 体量 >1KB 的文本 / 任何结构化数据 / 任何文件型产出
- emma 可能想保留给用户的成品

不写（**避免万能背包反模式**）：
- 自己下一轮就消费完的中间值——直接放 prompt 里
- 一句结论、一两个数字、布尔判断
- "反正能存就都存"的占位记录

调用时必须带：
- ` + "`type`" + `: ` + "`structured`" + ` / ` + "`file`" + ` / ` + "`blob`" + `
- ` + "`description`" + `: 一行讲清"这是什么"——下游靠这行决定要不要 read
- ` + "`schema`" + `: **结构化数据必填**（如 ` + "`{\"type\":\"table\",\"columns\":[\"month\",\"sales\"]}`" + `）。没有 schema 下游会瞎猜列含义，错误率飙升。

写完把返回的 ` + "`artifact_id`" + ` 放进你的 ` + "`<summary>`" + ` 或 tool_result，**只写 ID + 一句描述，不要把 content 再粘一遍**。

## 何时 ArtifactRead

收到任何 ` + "`artifact_id`" + ` 时，**先扫后取**，三档模式：

| mode | 返回 | 何时用 |
|------|------|--------|
| ` + "`metadata`" + ` | name/description/preview/size，不含 content | 想知道"这是什么"——决定要不要细看 |
| ` + "`preview`" + ` | metadata + 截断 preview（默认） | 几百字预览够回答问题就停在这 |
| ` + "`full`" + ` | 完整 content | 必须按字处理：逐行解析 CSV、重写、做 diff |

默认从 ` + "`preview`" + ` 开始。**只在确实要消费原始字节时升级到 ` + "`full`" + `**——大文件全文进 LLM 上下文几乎总是错的。

## 硬规则

- ` + "`artifact_id`" + ` **由 ArtifactWrite 返回，绝不能自己编**。看到一个不在你的产出 / 输入里的 ID，那就是幻觉，停下来汇报而不是猜测。
- 收到的 artifact 不要原样转发回 prompt——下游想要会自己去 read。
- 同一份逻辑数据要修改 → 用 ` + "`parent_artifact_id`" + ` 参数产生新 version，**不要原地覆盖**。老版本保留可回滚。
- 写完仅返 ID + 描述。重复粘贴 content 既浪费 token，又让消费者搞不清哪份是权威。

## Final Text 契约（极重要）

你的最终回复（assistant 消息正文）**只能包含：**
1. 一个 ` + "`<summary>`" + ` 块，**≤ 200 字**，描述过程要点和产出 ID 列表
2. 完成时调用 ` + "`SubmitTaskResult`" + ` 工具（如任务带 ` + "`<expected-outputs>`" + ` 契约则**必调**）

**不能包含**：
- 报告/表格/代码/分析正文 → 这些必须进 ` + "`ArtifactWrite`" + `
- 复制粘贴 artifact 的 preview 或 content → 让消费者自己 ` + "`ArtifactRead`" + `
- 用 markdown 把数据"展示"出来 → 同上

违反契约的后果：框架会拒绝任务完成、要求重做。每次重做都会消耗你的轮次预算。

**正确的最终回复样例：**
` + "```" + `
<summary>
完成 Q4 销量调研。已写入：
- art_xxx (findings_report): 5 个核心发现 + 来源
- art_yyy (data_table): 月度对比表 (CSV)
</summary>
` + "```" + `

**错误的最终回复样例（会被拒）：**
` + "```" + `
<summary>完成调研</summary>
以下是详细发现：
1. 2024 Q4 收入同比上升 20% ...   ← 正文不该在这里！
2. ...
` + "```" + ``
