# Changelog

| 版本 | 日期 | 变更 |
|------|------|------|
| 1.13 | 2026-05-02 | **Artifact 协议 + 契约式产出（向前兼容）**：把 L3 的产出从"LLM 自述"升级为"框架强校验"，覆盖设计文档八类失败模式中的前 5 种。改动只增不删，旧客户端忽略新字段即可。要点：(1) **新增工具 `ArtifactWrite` / `ArtifactRead` / `SubmitTaskResult`**：前两个是跨 Agent 共享数据的载体（按引用而非按值传递）；第三个是 L3 的"任务完成宣告"，强 schema (`artifacts.minItems≥1`、`summary.maxLength=200`) + 服务端校验（ID 存在、producer.task_id 匹配、created_at ≥ task_started_at、size>0、type/role 与 expected_outputs 匹配）；(2) **新增 `ArtifactRef` wire shape**（`artifact_id`/`name`/`type`/`mime_type`/`size_bytes`/`description`/`preview_text`/`uri`/`role`），见 §10.6；(3) **`tool.end` / `subagent.end` / `subagent.event(tool_end)` 新增 `artifacts: []ArtifactRef` 字段**：前者每条 ArtifactWrite 后填一条 Ref；后者按 SubmitTaskResult 通过的列表（有契约时）或全部 ArtifactWrite（兜底）聚合；(4) **`render_hint` 枚举扩 3 个值**：`artifact`（产出新 artifact）、`artifact_view`（读取已有 artifact）、`task_submission`（任务完成宣告）；(5) **`Task` 工具入参新增 `expected_outputs []ExpectedOutput`**：派发方声明产出契约（role/type/schema/min_size_bytes/required），见 §10.7；(6) **L3 loop 终止逻辑**：派发带契约时，end_turn 必须 SubmitTaskResult 通过验证；未通过 → 框架注入 SYSTEM 提醒（最多 3 次），仍不交则 status=`max_turns` + 错误说明；(7) **新增 render_hint 不破坏旧客户端**：未识别值 fallback 到 plain。详见 §6.4.4、§6.6、§7.15 |
| 1.12 | 2026-05-01 | **任务级可视化（向前兼容）**：把"sub-agent 在做什么"从黑盒变透明，三处增量改动，无 Breaking。要点：(1) **`subagent.start` 新增 `task` 字段**：携带父 Agent 派发给子 Agent 的完整 prompt（截断到 800 字），用户从启动那一刻就能看到 L3 接到的实际任务，而不只是 3-5 字的 `description` 短标签；(2) **新增 `agent.intent` 顶层事件**：主 Agent（emma）调用工具前的进度句（"正在搜索 vLLM 论文"）。框架在每个工具的 InputSchema 中**强制注入 `intent` 必填字段**——provider schema 验证保证模型必须填，不依赖 prompt 配合；ToolExecutor 在 `tool.start` 之前抽出 `intent` 发本事件，再剥掉字段交给真实工具；(3) **`subagent.event` 新增 `event_type: "intent"`**：sub-agent 内的 `agent_intent` 由 SpawnSync 转发循环包装为 `subagent.event{event_type: "intent", intent: "..."}`，与 `tool_start/tool_end` 同构，附 `agent_id` 标识属于哪一层。客户端可在每次工具调用前显示"researcher 正在 X"实时进度。空 intent / 缺字段时框架优雅降级（不发事件、不阻塞工具），保证向前兼容。详见 §6.6 |
| 1.11 | 2026-04-29 | **结构化 Emit 协议（向前兼容）**：新增 14 种生命周期事件，统一带 `envelope` / `display` / `metrics` 字段。所有事件由框架（确定性代码）触发，LLM 仅生成 display 素材。详见 §6.13。要点：(1) **命名空间**：plan-step 生命周期使用 `step.*`（不与 v1.7 用户级 TodoList 的 `task.*` 冲突），新增 `step.dispatched/started/progress/completed/failed/skipped`；(2) **Trace/Plan**：`trace.started/finished/failed` + `plan.created/updated/completed/failed`；(3) **Envelope**：`trace_id/seq/parent_event_id/task_id/parent_task_id/agent_role/agent_id/agent_run_id/severity`，`agent_role` 用职责命名（`persona/orchestrator/worker/system`）而非 L1/L2/L3 抽象层级；(4) **Envelope 加性**：emit 事件保留顶层 `event_id`/`session_id`，envelope 不重复，agent_id 仅在 envelope 内；(5) **Display 字段**：`title`/`summary`/`icon`/`visibility`，icon 受控枚举（`plan/dispatch/search/analysis/tool/success/error/warning/info/agent/task/step/default`），未识别值客户端 fallback 到 `default`；(6) **错误对齐**：emit 失败事件的 `error` 与 §6.12 同形（`type`/`code`/`message`/`user_message`/`retryable`），`type` 为受控枚举且加入 `tool_timeout/llm_timeout/agent_max_turns_exceeded/dependency_failed/orphan_timeout/aborted`；(7) **断线重连**：新增 `session.resume`（客户端）/ `session.resumed` / `session.resume_failed`（服务端），见 §3.6；(8) **版本号统一**：移除独立的 `schema_version`，emit 跟随 `protocol_version` 演进；(9) `session.created.capabilities` 新增 `emit: true` |
| 1.10 | 2026-04-29 | **L1/L2 隔离改造（Breaking）**：WebSocket 仅与 L1（emma 主体）通信，子 Agent 的 LLM 文本输出**不再**通过 `subagent.event` 转发。`subagent.event` 的 `event_type: "text"` 字段**废弃**——服务端不再发送该类型事件。仅保留 `tool_start` / `tool_end` 两种 inner event_type，用于工具执行过程的观测渲染。所有用户可见的对话文本现在仅来自 L1 的 `content.delta` 流。子 Agent 文本不再泄漏给客户端，避免与 emma 的总结回复重复。前端依赖 v1.8 子 Agent 文本流渐进渲染的实现需要移除 |
| 1.9 | 2026-04-22 | `tool.end` render hints：新增 `render_hint`、`language`、`file_path` 三个 top-level 字段，为客户端提供工具输出的语义渲染提示。工具在 `Metadata` 中设置这些 key，mapper 自动提升为 top-level 并从残余 metadata 中移除（与 `duration_ms` 模式一致）。新增 `RenderHint` 类型和 12 个常量值、`ExtToLanguage()` 文件扩展名→语言映射工具函数 |
| 1.8 | 2026-04-20 | 子 Agent 实时流式推送：新增 `subagent.event` 服务端事件，将子 Agent 内部的文本输出和工具执行过程实时流式转发给客户端。客户端无需等待 `subagent.end` 即可渐进渲染子 Agent 的产出。`subagent.event` 嵌套在 `subagent.start` 和 `subagent.end` 之间 |
| 1.7 | 2026-04-20 | 多 Agent 完整协议：Phase 2-5 事件从预留提升为正式协议。新增 `agent.routed`（@-mention 路由通知）、`task.created`/`task.updated`（任务系统）、`agent.message`（Agent 间通信）、`agent.spawned`/`agent.idle`/`agent.completed`/`agent.failed`（异步 Agent 生命周期）、`team.created`/`team.member_join`/`team.member_left`/`team.deleted`（团队编排）共 13 个新事件。`capabilities` 新增 `tasks`/`messaging`/`async_agent`/`teams` 字段。`subagent.start` 的 `agent_desc` 字段更名为 `description`。`subagent.end` 新增 `denied_tools` 字段 |
| 1.6 | 2026-04-20 | 多 Agent 可观测性协议（Phase 1）：新增 `subagent.start` / `subagent.end` 服务端事件，用于通知客户端同步子 Agent 的生命周期。`session.created` 的 `capabilities` 新增 `sub_agents` 字段。新增事件序列图（§7.10） |
| 1.5 | 2026-04-09 | `user.message` 的 `content` 字段扩展为支持多类型内容块数组：`text`（文本）、`image`（图片）、`file`（文件）。`image` 和 `file` 通过 `source` 对象描述数据来源，支持 `path`（本地路径）、`url`（远程 URL）、`base64`（内联数据）三种方式。同时保持向后兼容：`content` 仍可为单个对象，`text` 快捷字段仍有效 |
| 1.4 | 2026-04-08 | 连接协议改为显式握手：客户端必须发送 `session.create` 后才能收到 `session.created`，连接建立时服务端不再自动推送；新增 pre-init gate，初始化前仅接受 `session.create` 和 `ping`；权限审批增强为三选项模型（单次允许/会话级允许/拒绝）；会话级审批粒度为「程序 + 子命令」（如 `Bash:git push`），而非工具名或程序名；`permission.request` 新增 `options` + `permission_key` 字段；`permission.response` 新增 `scope` 字段；移除权限审批超时（无限等待直到用户操作） |
| 1.3 | 2026-04-08 | 新增权限审批协议（`permission.request`/`permission.response`）；服务端工具执行需要用户确认时，通过 WebSocket 异步审批而非直接拒绝 |
| 1.2 | 2026-04-07 | 新增服务端工具执行事件（`tool.start`/`tool.end`）；LLM tool_use 输出统一走 `content.*` 内容块；`EngineEventToolUse` 与 `EngineEventToolStart`/`EngineEventToolEnd` 职责分离 |
| 1.1 | 2026-04-07 | **Breaking**: 1) 新增客户端工具执行协议（`tool.call`/`tool.result`）；2) 工具安全管控协议（denied/timeout/cancelled）；3) `result` → `task.end`；4) `type` 字段统一为 `dot.notation` 风格（`message.start`/`content.start`/`content.delta`/`content.stop`/`message.delta`/`message.stop`） |
| 1.0 | 2026-04-07 | 初始版本。对齐 Anthropic streaming + OpenAI Realtime 协议设计 |

---

# WebSocket Channel Protocol v1.13

## 1. Overview

本协议定义了 harnessclaw-go WebSocket Channel 的双向通信规范。客户端通过 WebSocket 发送用户消息和控制指令，服务端以流式事件实时推送 query-loop 的完整执行过程。

**协议版本**: `1.13`

**v1.13 核心变更（向前兼容）**:
- **Artifact 协议落地**：跨 Agent 共享数据从"prompt 按值塞回去"升级为"按 ID 引用"。新增三个工具——`ArtifactWrite`（持久化产出，返回 `artifact_id`）、`ArtifactRead`（按 ID + mode=metadata|preview|full 取回）、`SubmitTaskResult`（L3 任务完成宣告）
- **`ArtifactRef` wire shape**（§10.6）：所有 Ref 出现在 `tool.end.artifacts` / `subagent.end.artifacts` / `subagent.event(tool_end).payload.artifacts` 上。字段 `artifact_id`/`name`/`type`/`mime_type`/`size_bytes`/`description`/`preview_text`/`uri`/`role`，前端按需用做产出物卡片渲染 + 下载链接构建
- **`render_hint` 枚举扩 3 个值**：`artifact`（producer 写了新 artifact）/ `artifact_view`（consumer 取了已有）/ `task_submission`（L3 提交任务结果，含通过/拒绝两路）。未识别值 fallback 到 `plain`
- **`Task` 工具新增 `expected_outputs` 入参**（§10.7）：派发方声明产出契约（`role`/`type`/`schema`/`min_size_bytes`/`required` 等）。框架基于此做服务端校验，强制 L3 实际写入而非"声称完成"
- **L3 loop 终止变化（仅契约模式）**：`expected_outputs` 非空时，`end_turn` 必须有一次 `SubmitTaskResult` 通过验证才算完成。未通过时框架最多注入 3 次 SYSTEM 提醒；仍不交则 `subagent.end.status = "max_turns"`，`Terminal.Message` 含 `"declined to call SubmitTaskResult"`
- **8 类失败模式覆盖率**：完全不写 / 编造 ID / 写空文件 / 双写泄漏 / 部分提交 / Schema 错配 / 滥用引用 共 7 类已守护；语义敷衍（#6）保留 Milestone B
- **协议侵入度**：旧客户端忽略 `artifacts` 字段、不识别 `task_submission` render_hint 即等价 v1.12；前端要利用新能力时按 §6.6 / §10.6 渲染产出卡片
- 详见 §6.4.4、§6.6、§7.15、§10.6、§10.7

**v1.12 核心变更（向前兼容）**:
- **任务级可视化**: 解决"L3 sub-agent 在做什么用户看不见"的核心痛点。改动只增不删，旧客户端忽略新字段即可。
- **`subagent.start` 新增 `task` 字段**：携带父 Agent 派发给子 Agent 的完整 prompt（按 rune 截断到 800 字），用户启动瞬间就看得到 L3 实际接到的任务文本，而不只是 3-5 字的 `description` 短标签
- **新增 `agent.intent` 顶层事件**：主 Agent（emma）每次调用工具之前的进度句（如"正在搜索 vLLM 论文"），先于 `tool.start` 到达
- **`subagent.event.payload.event_type` 新增 `"intent"`**：sub-agent 内部的 intent 经 SpawnSync 转发循环包装后到达，附 `agent_id` 标识属于哪一层（emma → `agent.intent`；researcher 等 → `subagent.event{intent}`）
- **强约束机制（关键）**: 框架在 **所有** 工具的 `InputSchema.properties` 中强制注入 `intent: string` 必填字段。LLM provider 的 schema 验证层把"必须填 intent"作为硬约束——模型不再可能"忘记"上报进度。`ToolExecutor` 在 `tool.start` 之前从 input 抽出 `intent` 并剥掉字段，真实工具拿到的 input 不含 `intent`，所以工具实现不需要任何改动
- **优雅降级**: provider 偶尔放宽校验时（缺 intent / 空字符串 / 非字符串值），框架不发 progress 事件但工具照常执行——用户漏看一句进度好过整条链路阻塞
- **协议侵入度为零**: 工具开发者不动；前端旧客户端忽略 `task` / `agent.intent` / `event_type=intent` 即等价于 v1.11 行为
- 详见 §6.6

**v1.11 核心变更（向前兼容）**:
- **结构化 Emit 协议**: 新增 14 种生命周期事件，统一携带 `envelope` / `display` / `metrics` 三段元数据
  - **Trace（请求）**: `trace.started` / `trace.finished` / `trace.failed`
  - **Plan（编排）**: `plan.created` / `plan.updated` / `plan.completed` / `plan.failed`
  - **Step（编排步骤）**: `step.dispatched` / `step.started` / `step.progress` / `step.completed` / `step.failed` / `step.skipped`
  - **Agent**: `agent.heartbeat`
- **命名空间隔离**: 编排步骤使用 `step.*` 命名空间，与 §6.8 的用户级 TodoList `task.*` **完全分离**——前端可按 `step.` 前缀做一级 switch 而不会和 todo 列表冲突
- **Envelope 元数据**: 每个新事件带 `trace_id` / `seq` / `parent_event_id` / `task_id` / `parent_task_id` / `agent_role` / `agent_id` / `agent_run_id` / `severity`，让客户端能在乱序到达和断线重连情况下重建执行树
  - `agent_role` 取值 `persona` / `orchestrator` / `worker` / `system`（**职责命名**，不暴露 L1/L2/L3 内部架构层级，便于未来重构）
  - **Envelope 是加性结构**：emit 事件仍保留 §4 规定的顶层 `event_id` / `session_id`；envelope 不重复这两个字段，`agent_id` 仅在 envelope 内（避免与 §6.6 / §6.10 的顶层 `agent_id` 重复歧义）
  - **没有独立的 `schema_version`**：emit 协议跟随 `protocol_version` 演进，单一版本号
- **Display 字段**: 直接提供 `title` / `summary` / `icon` / `visibility`，前端无需解析 payload 即可渲染卡片
  - `icon` 是**受控枚举**：`plan` / `dispatch` / `search` / `analysis` / `tool` / `success` / `error` / `warning` / `info` / `agent` / `task` / `step` / `default`；服务端 MAY 增加新值，客户端 MUST 在不识别时 fallback 到 `default`
- **Metrics 字段**: 终态事件带 `duration_ms` / `tokens_in` / `tokens_out` / `cost_usd`，供成本归因和性能分析
- **错误结构对齐 §6.12**: emit 失败事件（`trace.failed` / `plan.failed` / `step.failed`）的 `error` 与 §6.12 `error` 同形（`type` / `code` / `message` / `user_message` / `retryable`）
  - `error.type` 是**受控枚举**：复用 §6.12 的 7 种连接级错误，**新增** `tool_timeout` / `tool_rate_limited` / `tool_invalid_input` / `llm_timeout` / `llm_content_filter` / `agent_max_turns_exceeded` / `dependency_failed` / `orphan_timeout` / `aborted`
  - `error.code` 是开发者自由码（如 `BASH_TIMEOUT`），用于精细化定位；客户端不依赖
  - `error.user_message` 是 persona-friendly 文案，L1 引用此字段而非原始 `message`
- **断线重连协议**（§3.6）: 客户端发送 `session.resume` 携带 `trace_id` + `last_seq`；服务端在保留窗口内补发 `seq > last_seq` 的事件并以 `session.resumed` 收尾；超出留存窗口则 `session.resume_failed`，客户端走全量刷新
- **生命周期闭环**: 任何 `*.started` / `*.dispatched` / `*.created` 必有对应的 `*.finished` / `*.completed` / `*.failed` / `*.skipped`；异常/孤儿场景由框架兜底 emit `*.failed`（`error.type=orphan_timeout`）
- **Capabilities 扩展**: `session.created.capabilities` 新增 `emit: true`
- **触发机制**: emit 由框架（确定性代码）触发，LLM 只能生成 `display.title` 等素材
- **向前兼容**: 现有事件类型（`content.*` / `tool.*` / `subagent.*` / `agent.*` / `task.*` TodoList / `team.*`）保持不变，旧客户端忽略新事件即可

**v1.10 核心变更（Breaking）**:
- **L1/L2 隔离架构**: WebSocket 现在仅与 L1（用户面，emma）通信。子 Agent（L2，搭档）的 LLM 文本输出由 emma 内部读取并润色后再呈现给用户——**子 Agent 的原文不再发送到客户端**
- **`subagent.event` 的 `event_type: "text"` 废弃**: 服务端不再发送子 Agent 的文本流事件
- **保留观测事件**: `event_type: "tool_start"` 和 `"tool_end"` 仍然转发，客户端可据此渲染 "小林 正在写文件 X.md" 之类的工具执行状态
- **用户对话文本来源唯一**: 所有用户可见的对话文本现在仅来自 L1 主体（emma）的 `content.delta` 流
- **前端迁移**: 依赖 v1.8 子 Agent 文本流渐进渲染的客户端代码需要移除——子 Agent 已不再发送 text 类型事件

**v1.9 核心变更**:
- **`tool.end` Render Hints**: `tool.end` 新增三个 top-level 字段：`render_hint`（渲染类型）、`language`（语法高亮语言）、`file_path`（关联文件路径），引导客户端按工具类型选择渲染组件
- **RenderHint 类型**: `terminal` / `code` / `diff` / `file_info` / `search` / `markdown` / `agent` / `skill` / `task` / `message` / `team` / `plain`
- **Promote-and-dedup**: 三个新字段与已有 `duration_ms` 遵循相同模式 — 工具在 `Metadata` 中设置 key，mapper 提升到 top-level 并从残余 metadata 中移除
- **ExtToLanguage**: 新增共享工具函数，将文件扩展名映射为语法高亮语言 ID（35+ 扩展名），供 Read/Edit/Write 工具使用
- **向后兼容**: 三个新字段均使用 `omitempty` JSON tag，不影响旧客户端

**v1.8 核心变更**:
- **子 Agent 实时流式推送**: 新增 `subagent.event` 服务端事件（§6.6），将子 Agent 内部的文本输出和工具执行过程实时流式转发给客户端
- **渐进渲染**: 客户端无需等待 `subagent.end` 即可逐步渲染子 Agent 的文本产出和工具调用状态
- **注意**: 原有 `subagent.start`/`subagent.end` 生命周期事件不受影响，`subagent.event` 嵌套在两者之间

**v1.7 核心变更**:
- **多 Agent 完整协议**: Phase 2-5 事件从预留提升为正式协议，新增 13 个事件类型
- **@-mention 路由**: 新增 `agent.routed` 事件，通知客户端 @-mention 路由到特定 Agent
- **任务系统**: 新增 `task.created`/`task.updated` 事件（§6.8），支持任务创建和状态变更通知
- **Agent 间通信**: 新增 `agent.message` 事件（§6.9），客户端可观测 Agent 间消息传递
- **异步 Agent**: 新增 `agent.spawned`/`agent.idle`/`agent.completed`/`agent.failed` 事件（§6.10），完整异步 Agent 生命周期
- **团队编排**: 新增 `team.created`/`team.member_join`/`team.member_left`/`team.deleted` 事件（§6.11），团队生命周期管理
- **Capabilities 扩展**: `session.created` 的 `capabilities` 新增 `tasks`/`messaging`/`async_agent`/`teams` 字段
- **`subagent.start` 字段变更**: `agent_desc` 更名为 `description`（**Breaking**）
- **`subagent.end` 字段新增**: 新增 `denied_tools` 数组字段

**v1.6 核心变更**:
- **子 Agent 可观测性**: 新增 `subagent.start` 和 `subagent.end` 服务端事件，客户端可感知同步子 Agent 的启动和完成
- **Capabilities 扩展**: `session.created` 的 `capabilities` 新增 `sub_agents` 布尔字段，声明服务端是否支持子 Agent 生成
- **前向兼容**: 客户端应忽略不识别的 `subagent.*` 事件（按既有前向兼容约定）

**v1.5 核心变更**:
- **多类型内容块**: `user.message` 的 `content` 字段从单一对象扩展为支持内容块数组，包含 `text`、`image`、`file` 三种类型
- **image/file 数据来源**: 通过 `source` 对象描述，支持 `path`（本地路径）、`url`（远程 URL）、`base64`（内联 base64 编码数据）三种方式
- **向后兼容**: `content` 仍可为单个对象（v1.4 格式），`text` 快捷字段仍有效

**v1.4 核心变更**:
- **显式会话初始化**: 连接建立后服务端不再自动推送 `session.created`。客户端 **MUST** 发送 `session.create` 请求来初始化会话，服务端收到后回复 `session.created`
- **Pre-init gate**: 连接初始化前仅接受 `session.create` 和 `ping` 两种消息类型，其他消息将收到 `error` 帧并被丢弃
- **三选项权限模型**: 用户可选择「单次允许」「会话级允许」「拒绝」三种审批方式
- **`permission.request` 新增 `options`**: 服务端向客户端提供可选操作列表，客户端据此渲染审批 UI
- **`permission.response` 新增 `scope`**: 客户端通过 `scope` 字段告知服务端审批范围（`"once"` 或 `"session"`）
- **会话级自动审批**: 用户选择「会话级允许」后，同一会话内相同工具的后续调用将自动审批，不再弹出确认
- **移除审批超时**: 权限审批无时间限制，服务端无限等待直到用户操作或会话中断

**v1.3 核心变更**:
- **权限审批协议**: 新增 `permission.request`（服务端→客户端）和 `permission.response`（客户端→服务端）事件，用于工具执行权限的异步审批
- **非中断式权限检查**: 当工具执行需要用户确认时（如写操作），服务端不再直接拒绝，而是发送 `permission.request` 到客户端，等待用户审批后继续或中止执行

**v1.2 核心变更**:
- **服务端工具执行事件**: 新增 `tool.start` 和 `tool.end` 事件，用于通知客户端服务端工具执行的开始和结束
- **LLM 输出与工具执行分离**: LLM 输出的 `tool_use` 内容块统一走 `content.start` → `content.delta` → `content.stop` 流程（与 `text` 块一致），不再与工具执行事件耦合
- **`EngineEventToolUse` 语义明确**: 仅表示 LLM 输出了 tool_use 内容块，不涉及执行

**v1.1 核心变更**:
- **客户端工具执行**: 工具（如 bash）在客户端本地执行，服务端通过 `tool.call` 下发调用请求，客户端通过 `tool.result` 回传结果
- **工具安全管控**: 工具执行可能因权限、超时、用户取消等原因中止，协议支持结构化的状态上报
- **`result` → `task.end`**: 明确表示 query-loop 任务结束语义
- **`type` 统一为 `dot.notation`**: 所有事件类型采用 `namespace.action` 格式

**设计对齐**:
- 流式事件结构对齐 [Anthropic Messages Streaming API](https://docs.anthropic.com/en/api/messages-streaming)
- 会话管理参考 [OpenAI Realtime API](https://platform.openai.com/docs/guides/realtime)
- 工具执行模型采用本地工具调用机制

**前向兼容约定**:
- 客户端 **MUST** 忽略不识别的 `type` 值，跳过该消息继续处理
- 客户端 **MUST** 忽略任何消息中不识别的字段
- 服务端 **MAY** 在任何消息中添加新字段，不视为 breaking change
- 新增 `type` 不视为 breaking change；移除或修改已有 `type` 语义为 breaking change

---

## 2. Authentication

连接时通过 HTTP 升级请求的 header 传递认证信息：

```
GET /v1/ws HTTP/1.1
Authorization: Bearer <api_key>
Upgrade: websocket
```

| 方式 | Header | 说明 |
|------|--------|------|
| API Key | `Authorization: Bearer <key>` | 推荐方式 |
| Token | `X-Auth-Token: <token>` | 短期令牌（由 REST API 签发） |

认证失败时服务端返回 HTTP 401 拒绝升级，不会建立 WebSocket 连接。

> **Note**: 当前开发阶段认证为 pass-through，所有请求放行。生产部署前将强制启用。

---

## 3. Connection

### 3.1 Endpoint

```
ws://{host}:{port}/v1/ws
```

默认地址：`ws://0.0.0.0:8081/v1/ws`

URL path 中的 `v1` 为协议主版本号，不同主版本为不兼容协议。

> **Note**: v1.4 起，`session_id` 和 `user_id` 不再通过 URL query 参数传递，改为在 `session.create` 消息中指定。

### 3.2 Handshake

WebSocket 连接建立后，连接处于**未初始化**状态。客户端 **MUST** 发送 `session.create` 消息来初始化会话：

1. 客户端发送 `session.create`（§5.0），可选指定 `session_id` 和 `user_id`
2. 服务端处理后回复 `session.created`（§6.1），携带分配的 `session_id` 和服务端能力
3. 客户端收到 `session.created` 后，方可发送其他消息

**Pre-init gate**: 初始化完成前，仅接受 `session.create` 和 `ping` 两种消息。发送其他类型的消息将收到 `error` 帧（code: `session_not_initialized`）。已初始化后再次发送 `session.create` 将收到 `error` 帧（code: `session_already_created`）。

```
Client                                Server
  |                                     |
  |--- WebSocket Upgrade Request ------>|
  |<-- 101 Switching Protocols ---------|
  |                                     |
  |--- session.create ---------------->|  (session_id, user_id 可选)
  |<-- session.created ----------------|  (分配的 session_id, 能力声明)
  |                                     |
  |--- user.message ------------------>|  (现在可以发消息了)
  |<-- message.start ------------------|
  |<-- content.start ------------------|
  |<-- content.delta ------------------|
  |       ...                           |
```

### 3.3 Multiple Connections

同一 `session_id` 支持多个 WebSocket 连接（viewer 模式）。所有连接收到相同的服务端事件。任一连接发送的消息对所有连接可见。

### 3.4 Disconnection

- **客户端主动断开**: 发送 WebSocket Close frame（status 1000）
- **服务端关闭**: 发送 Close frame + reason text，客户端应尝试重连
- **异常断开**: 客户端应实现指数退避重连（建议初始 1s，最大 30s）

### 3.5 Keep-Alive

服务端每 30 秒发送 WebSocket 协议级 Ping frame。客户端 **MUST** 按 WebSocket 协议自动回复 Pong（大多数 WebSocket 库自动处理）。

### 3.6 Reconnect & Event Resume (v1.11+)

WebSocket 断线在生产环境是常态。Emit 协议带了 `trace_id` 和 `seq`，理论上具备续传能力。v1.11 起协议正式定义了三态续传流程：

**客户端流程**:
1. 检测到连接断开 → 按 §3.4 指数退避重连
2. 重连成功后发送 `session.create`（携带原 `session_id`）
3. 收到 `session.created` 后，对每个仍在跟踪的 trace 发送一条 `session.resume`（§5.7）：
   ```json
   {
     "type": "session.resume",
     "event_id": "evt_client_002",
     "session_id": "sess_abc123",
     "trace_id": "tr_xxx",
     "last_seq": 12
   }
   ```
4. 等待服务端响应：
   - **`session.resumed`**（§6.14）：进入正常补发模式，服务端会按原序投递 `seq > last_seq` 的事件
   - **`session.resume_failed`**（§6.14）：续传不可行，客户端应**丢弃**该 trace 的本地状态并按 REST 历史 API 全量刷新

**服务端契约**:
- **留存窗口**: 服务端 SHOULD 保留每条 trace 最近 5 分钟的事件流（含 envelope）
- **幂等性**: 客户端 MUST 按 `event_id` 去重；同一个事件可能被补发也可能被原通道交付一次
- **顺序**: 补发严格按 `seq` 升序；补发期间到达的新事件会排在补发尾部
- **失败原因**: `session.resume_failed.reason` 取值：`events_expired`（超出留存窗口） / `unknown_trace`（trace_id 不存在） / `session_not_found`（session 已被回收） / `not_implemented`（服务端未启用留存）

**客户端 fallback 策略**:
- 收到 `not_implemented` 或 `events_expired` → 把 in-memory trace 状态清空，触发 REST 历史 API 拉全量
- 收到 `unknown_trace` → 当作该 trace 已结束（绝大多数情况是用户在断线前就结束了）

**当前实现状态**: 协议契约已稳定，留存缓冲区为下一阶段实现；v1.11 服务端总是回复 `session.resume_failed { reason: "not_implemented" }`，客户端应直接走全量刷新路径。

---

## 4. Message Format

所有消息为 JSON 文本帧（WebSocket text frame），共享以下公共字段：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | 事件类型，客户端一级 switch 的唯一依据 |
| `event_id` | string | 是 | 消息唯一 ID（服务端: `evt_` 前缀自动生成；客户端: 自行生成） |
| `session_id` | string | 条件 | 除 `ping` 外所有消息都携带 |

**命名规范**:
- `type` 值统一使用 `dot.notation`（`namespace.action`）格式
- 所有 JSON 字段名使用 `snake_case`

**Emit 事件与顶层字段的关系**（v1.11+ 必须严格遵守）:
- §6.13 的 emit 生命周期事件（`trace.*` / `plan.*` / `step.*` / `agent.heartbeat`）**仍然**带顶层 `event_id` 和 `session_id`，envelope **不替代**它们
- 顶层 `event_id` **就是** envelope 的事件唯一 ID — 同一条消息**只有一个** `event_id`，envelope 里没有 `event_id` 字段（避免重复声明）
- 顶层 `session_id` 仍然存在
- emit 事件的 `agent_id` **只在** envelope 内出现；§6.6 / §6.10 的 `subagent.*` / `agent.spawned` 等既有事件保留它们的顶层 `agent_id`，**不引入** envelope（保持向前兼容）
- 一句话规则：**新事件用 envelope 携带元数据；旧事件原样不动**

---

## 5. Client Events

### 5.0 `session.create` — 初始化会话

连接建立后客户端 **MUST** 发送此消息来初始化会话。服务端收到后回复 `session.created`（§6.1）。

```json
{
  "type": "session.create",
  "event_id": "evt_client_000",
  "session_id": "sess_abc123",
  "user_id": "user_001"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `session_id` | string | 否 | 会话 ID。不传则服务端生成 UUID |
| `user_id` | string | 否 | 用户标识，用于权限校验和审计 |

**注意**:
- 每个连接只能发送一次 `session.create`。重复发送将收到 `error` 帧（code: `session_already_created`）
- 初始化完成前发送非 `session.create`/`ping` 消息将收到 `error` 帧（code: `session_not_initialized`）
- 同一 `session_id` 支持多个连接（viewer 模式），每个连接独立发送 `session.create`

### 5.1 `user.message` — 发送用户消息

`content` 字段支持三种格式：

**格式 1: 快捷文本（仅文本）**

```json
{
  "type": "user.message",
  "event_id": "evt_client_001",
  "text": "What is 1+1?"
}
```

**格式 2: 单内容块对象（v1.4 兼容）**

```json
{
  "type": "user.message",
  "event_id": "evt_client_001",
  "content": {
    "type": "text",
    "text": "What is 1+1?"
  }
}
```

**格式 3: 多内容块数组（v1.5）**

```json
{
  "type": "user.message",
  "event_id": "evt_client_001",
  "content": [
    {"type": "text", "text": "请描述这张图片并分析这个文件"},
    {"type": "image", "source": {"type": "path", "path": "/tmp/screenshot.png"}},
    {"type": "file", "source": {"type": "url", "url": "https://example.com/data.csv", "media_type": "text/csv"}}
  ]
}
```

#### 内容块类型

| 类型 | 说明 | 必填字段 |
|------|------|----------|
| `text` | 纯文本 | `text` |
| `image` | 图片（支持 PNG/JPEG/GIF/WebP） | `source` |
| `file` | 文件附件（任意类型） | `source` |

#### Source 对象

`image` 和 `file` 类型通过 `source` 对象描述数据来源：

| source.type | 说明 | 必填字段 | 示例 |
|-------------|------|----------|------|
| `path` | 本地文件路径 | `path` | `{"type":"path","path":"/home/user/img.png"}` |
| `url` | 远程 URL | `url` | `{"type":"url","url":"https://example.com/file.pdf"}` |
| `base64` | 内联 base64 数据 | `data`, `media_type` | `{"type":"base64","media_type":"image/png","data":"iVBOR..."}` |

| Source 字段 | 类型 | 说明 |
|------------|------|------|
| `type` | string | 数据来源类型: `"path"` / `"url"` / `"base64"` |
| `path` | string | 本地文件系统路径（`type: "path"` 时必填） |
| `url` | string | 远程 URL（`type: "url"` 时必填） |
| `data` | string | Base64 编码数据（`type: "base64"` 时必填） |
| `media_type` | string | MIME 类型（如 `"image/png"`、`"text/csv"`）。`base64` 时必填，`path`/`url` 时可选 |

#### 字段总表

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `content` | object \| array | 否 | 单个内容块对象或内容块数组。与 `text` 二选一 |
| `text` | string | 否 | 纯文本快捷方式，等价于 `[{"type":"text","text":"..."}]` |

> **优先级**: `content` 优先于 `text`。两者都存在时，忽略 `text`。

`event_id` 将作为 `request_id` 出现在该请求触发的所有服务端事件中。

### 5.2 `tool.result` — 上报工具执行结果

客户端本地执行工具后，通过此事件将结果回传给服务端，供下一轮 LLM 调用使用。

```json
{
  "type": "tool.result",
  "event_id": "evt_client_010",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_001",
  "status": "success",
  "output": "total 48\ndrwxr-xr-x  12 user staff  384 Apr  7 10:00 .\n..."
}
```

**错误/中止场景**:

```json
{
  "type": "tool.result",
  "event_id": "evt_client_011",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_001",
  "status": "denied",
  "error": {
    "code": "permission_denied",
    "message": "User denied execution of bash command: rm -rf /"
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `tool_use_id` | string | 是 | 对应 `tool.call` 中的 `tool_use_id` |
| `status` | string | 是 | 执行状态（见下表） |
| `output` | string | 条件 | `status: "success"` 时必填，工具输出内容 |
| `error` | object | 条件 | `status` 非 `"success"` 时必填 |
| `error.code` | string | 是 | 机器可读错误码 |
| `error.message` | string | 是 | 人类可读描述 |
| `metadata` | object | 否 | 工具执行元数据（如 exit_code, duration_ms） |

**status 枚举**:

| status | 说明 | 服务端处理 |
|--------|------|-----------|
| `success` | 工具执行成功 | 将 output 作为 tool_result 传给 LLM |
| `error` | 工具执行报错（如命令返回非 0 exit code） | 将 error 信息传给 LLM，由模型决定下一步 |
| `denied` | 用户或安全策略拒绝执行 | 将 denied 信息传给 LLM，模型不应重试同一操作 |
| `timeout` | 执行超时（客户端 kill 进程） | 将 timeout 信息传给 LLM |
| `cancelled` | 用户手动取消执行 | 将 cancelled 信息传给 LLM |

**metadata 示例**（bash 工具）:

```json
{
  "metadata": {
    "exit_code": 0,
    "duration_ms": 1234,
    "truncated": false
  }
}
```

### 5.3 `permission.response` — 回复权限审批请求

客户端收到 `permission.request`（§6.4.3）后，通过此事件回传审批结果。支持三种选择：

1. **单次允许** — 仅允许本次工具调用
2. **会话级允许** — 允许本次调用，且同一会话内相同工具的后续调用自动审批
3. **拒绝** — 拒绝本次工具调用

**单次允许**:

```json
{
  "type": "permission.response",
  "event_id": "evt_client_030",
  "session_id": "sess_abc123",
  "request_id": "perm_a1b2c3d4",
  "approved": true,
  "scope": "once"
}
```

**会话级允许**（同一会话内相同工具不再询问）:

```json
{
  "type": "permission.response",
  "event_id": "evt_client_030",
  "session_id": "sess_abc123",
  "request_id": "perm_a1b2c3d4",
  "approved": true,
  "scope": "session"
}
```

**拒绝执行**:

```json
{
  "type": "permission.response",
  "event_id": "evt_client_031",
  "session_id": "sess_abc123",
  "request_id": "perm_a1b2c3d4",
  "approved": false,
  "message": "User denied: destructive operation"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `request_id` | string | 是 | 对应 `permission.request` 中的 `request_id` |
| `approved` | bool | 是 | `true` 批准执行，`false` 拒绝执行 |
| `scope` | string | 否 | 审批范围。`"once"`（默认）仅本次生效；`"session"` 本会话内相同工具自动审批 |
| `message` | string | 否 | 拒绝原因（`approved: false` 时建议填写） |

**超时**: 服务端不设超时。权限审批无限等待，直到用户操作或会话中断（`session.interrupt` / 连接断开）。

### 5.4 `session.update` — 更新会话配置（预留）

```json
{
  "type": "session.update",
  "event_id": "evt_client_002",
  "session_id": "sess_abc123",
  "session": {
    "model": "claude-sonnet-4-20250514",
    "system_prompt": "You are a helpful assistant.",
    "max_tokens": 4096
  }
}
```

> **Note**: 当前版本暂未实现，预留用于后续动态切换模型、修改 system prompt 等场景。

### 5.5 `session.interrupt` — 中断执行

```json
{
  "type": "session.interrupt",
  "event_id": "evt_client_003",
  "session_id": "sess_abc123"
}
```

中断当前 session 正在执行的 query-loop。服务端收到后尽快停止流式输出和工具执行，并发送 `task.end`（status: `aborted`）。

### 5.6 `ping` — 客户端心跳

```json
{"type": "ping", "event_id": "evt_client_004"}
```

服务端回复 `pong`。通常无需手动发送，WebSocket 协议层 Ping/Pong 已覆盖。

### 5.7 `session.resume` — 续传指定 trace 的事件流（v1.11+）

WebSocket 重连后，对每个仍在跟踪的 trace 发送一条续传请求。语义和留存契约见 §3.6。

```json
{
  "type": "session.resume",
  "event_id": "evt_client_005",
  "session_id": "sess_abc123",
  "trace_id": "tr_xxx",
  "last_seq": 12
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `trace_id` | string | 是 | 要续传的 trace ID，对应之前 emit 事件 envelope 里的 `trace_id` |
| `last_seq` | int64 | 是 | 客户端实际收到的最后一条该 trace 事件的 `envelope.seq`；服务端补发 `seq > last_seq` 的事件 |

服务端响应：`session.resumed`（§6.14，成功）或 `session.resume_failed`（§6.14，失败）。

---

## 6. Server Events

### 6.1 Session Lifecycle

#### `session.created` — 会话初始化完成

客户端发送 `session.create`（§5.0）后，服务端回复此消息确认会话初始化成功。携带分配的会话 ID 和服务端能力声明。

```json
{
  "type": "session.created",
  "event_id": "evt_001",
  "session_id": "sess_abc123",
  "protocol_version": "1.11",
  "session": {
    "model": "claude-sonnet-4-20250514",
    "capabilities": {
      "streaming": true,
      "tools": true,
      "client_tools": true,
      "thinking": false,
      "multi_turn": true,
      "image_input": false,
      "sub_agents": true,
      "tasks": true,
      "messaging": true,
      "async_agent": true,
      "teams": true,
      "emit": true
    }
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `protocol_version` | string | 协议版本 |
| `session.model` | string | 当前使用的模型 |
| `session.capabilities` | object | 服务端能力声明 |
| `session.capabilities.client_tools` | bool | 是否启用客户端工具执行。`true` 时使用 `tool.call`/`tool.result` 流程；`false` 时使用 `tool.start`/`tool.end` 流程 |
| `session.capabilities.sub_agents` | bool | 是否支持子 Agent 生成。`true` 时客户端可能收到 `subagent.start`/`subagent.event`/`subagent.end` 事件 |
| `session.capabilities.tasks` | bool | 是否支持任务系统。`true` 时客户端可能收到 `task.created`/`task.updated` 事件 |
| `session.capabilities.messaging` | bool | 是否支持 Agent 间通信。`true` 时客户端可能收到 `agent.message` 事件 |
| `session.capabilities.async_agent` | bool | 是否支持异步 Agent。`true` 时客户端可能收到 `agent.spawned`/`agent.idle`/`agent.completed`/`agent.failed` 事件 |
| `session.capabilities.teams` | bool | 是否支持团队模式。`true` 时客户端可能收到 `team.*` 事件 |
| `session.capabilities.emit` | bool | 是否启用结构化 Emit 协议（v1.11+）。`true` 时客户端可能收到 `trace.*`/`plan.*`/`task.dispatched/progress/completed/failed/skipped`/`agent.heartbeat` 事件，每条都带 `envelope`/`display`/`metrics` |

#### `session.updated` — 配置变更确认

```json
{
  "type": "session.updated",
  "event_id": "evt_002",
  "session_id": "sess_abc123",
  "session": {
    "model": "claude-sonnet-4-20250514"
  }
}
```

#### `pong` — 心跳响应

```json
{"type": "pong", "event_id": "evt_003"}
```

### 6.2 Message Lifecycle

一次 LLM 调用产出一组消息事件。query-loop 中每轮 LLM 调用都独立产出一组完整的 `message.start` → ... → `message.stop` 序列。

#### `message.start` — 消息开始

每次 LLM 调用开始流式输出时推送，携带消息 ID、模型和 input usage。

```json
{
  "type": "message.start",
  "event_id": "evt_010",
  "session_id": "sess_abc123",
  "request_id": "evt_client_001",
  "message": {
    "id": "msg_001",
    "model": "claude-sonnet-4-20250514",
    "role": "assistant",
    "usage": {
      "input_tokens": 150,
      "output_tokens": 0,
      "cache_read_tokens": 0,
      "cache_write_tokens": 0
    }
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `request_id` | string | 对应触发请求的客户端 `event_id` |
| `message.id` | string | 消息唯一 ID（`msg_` 前缀） |
| `message.model` | string | 实际使用的模型 |
| `message.role` | string | 固定 `"assistant"` |
| `message.usage` | object | input token 统计（output 此时为 0） |

#### `message.delta` — 消息元数据更新

所有内容块结束后、`message.stop` 之前推送，携带最终的 `stop_reason` 和 output usage。

```json
{
  "type": "message.delta",
  "event_id": "evt_020",
  "session_id": "sess_abc123",
  "delta": {
    "stop_reason": "end_turn"
  },
  "usage": {
    "output_tokens": 42
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `delta.stop_reason` | string | `"end_turn"` / `"tool_use"` / `"max_tokens"` |
| `usage.output_tokens` | int | 本次调用的 output token 数 |

**stop_reason 枚举**:

| stop_reason | 说明 |
|-------------|------|
| `end_turn` | 模型自然结束 |
| `tool_use` | 模型请求调用工具 |
| `max_tokens` | 达到 output token 上限 |

#### `message.stop` — 消息完成

标记本次 LLM 调用的流式输出结束。无额外 payload。

```json
{
  "type": "message.stop",
  "event_id": "evt_021",
  "session_id": "sess_abc123"
}
```

### 6.3 Content Block Events

内容块事件嵌套在 `message.start` 和 `message.stop` 之间，描述具体的文本、工具调用、思考过程。

#### `content.start` — 内容块开始

```json
{
  "type": "content.start",
  "event_id": "evt_011",
  "session_id": "sess_abc123",
  "index": 0,
  "content_block": {
    "type": "text",
    "text": ""
  }
}
```

**content_block.type 枚举**:

| type | 说明 | 附加字段 |
|------|------|---------|
| `text` | 文本内容 | `text` (初始为空串) |
| `tool_use` | 工具调用 | `id`, `name`, `input` (初始 `{}`) |
| `thinking` | 扩展思考 | `thinking` (初始为空串) |
| `server_tool_use` | 服务端工具 (预留) | `id`, `name` |

**文本块**:
```json
{"type": "text", "text": ""}
```

**工具调用块**:
```json
{"type": "tool_use", "id": "toolu_001", "name": "bash", "input": {}}
```

**思考块** (预留):
```json
{"type": "thinking", "thinking": ""}
```

#### `content.delta` — 内容块增量

```json
{
  "type": "content.delta",
  "event_id": "evt_012",
  "session_id": "sess_abc123",
  "index": 0,
  "delta": {
    "type": "text_delta",
    "text": "Hello"
  }
}
```

**delta.type 枚举**:

| delta.type | 适用 block type | 字段 | 说明 |
|------------|----------------|------|------|
| `text_delta` | `text` | `text` | 文本增量片段 |
| `input_json_delta` | `tool_use` | `partial_json` | 工具输入 JSON 片段（字符串拼接） |
| `thinking_delta` | `thinking` | `thinking` | 思考过程增量片段 |

**文本增量**:
```json
{"type": "text_delta", "text": "Hello"}
```

**工具输入增量**:
```json
{"type": "input_json_delta", "partial_json": "{\"command\":"}
```

**思考增量** (预留):
```json
{"type": "thinking_delta", "thinking": "Let me analyze..."}
```

#### `content.stop` — 内容块结束

```json
{
  "type": "content.stop",
  "event_id": "evt_013",
  "session_id": "sess_abc123",
  "index": 0
}
```

### 6.4 Tool Execution Events

工具执行分为两种模式：**客户端执行**（`tool.call` → `tool.result`）和**服务端执行**（`tool.start` → `tool.end`）。

无论哪种模式，LLM 输出的 `tool_use` 内容块始终通过 `content.*` 事件传递（§6.3），与工具执行事件是独立的。

#### 6.4.1 客户端工具执行

当 LLM 输出 `stop_reason: "tool_use"` 时，服务端向客户端下发工具调用请求，客户端本地执行后回传结果。

#### `tool.call` — 下发工具调用请求

`message.stop`（`stop_reason: "tool_use"`）之后，服务端为每个待执行的工具调用发送一条 `tool.call`。

```json
{
  "type": "tool.call",
  "event_id": "evt_050",
  "session_id": "sess_abc123",
  "request_id": "evt_client_001",
  "tool_use_id": "toolu_001",
  "tool_name": "bash",
  "input": {
    "command": "ls -la"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `request_id` | string | 对应触发请求的客户端 `event_id` |
| `tool_use_id` | string | 工具调用唯一 ID（对应 `content_block` 中的 `id`） |
| `tool_name` | string | 工具名称 |
| `input` | object | 工具输入参数（从 LLM 输出的 `tool_use` 块中提取） |

客户端收到 `tool.call` 后 **MUST**：
1. 根据本地安全策略判断是否允许执行
2. 执行工具或拒绝执行
3. 通过 `tool.result`（§5.2）回传结果

**超时**: 服务端为每个 `tool.call` 设置超时（可配置，默认 120s）。超时未收到 `tool.result` 时，服务端视为 timeout 并自行构造 timeout 结果传给 LLM。

#### `tool.progress` — 工具执行心跳

长时间运行的工具可由客户端发送进度心跳，通知服务端工具仍在执行中（防止服务端超时）。

```json
{
  "type": "tool.progress",
  "event_id": "evt_client_020",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_001",
  "output": "Compiling... 50%"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `tool_use_id` | string | 是 | 对应 `tool.call` 中的 `tool_use_id` |
| `output` | string | 否 | 中间输出（如编译进度、部分日志） |

服务端收到心跳后重置该工具调用的超时计时器。

#### `tool.call` 特殊工具：AskUserQuestion

当 `tool_name` 为 `"AskUserQuestion"` 时，**这不是一个需要客户端"执行"的工具调用，而是 emma 主动向用户提问**。客户端 **MUST** 把它渲染成问题 UI（而非执行普通工具的代码路径），等待用户答复，然后用 `tool.result` 回传用户的回答。

`input` 结构：

```json
{
  "type": "tool.call",
  "tool_use_id": "toolu_ask_001",
  "tool_name": "AskUserQuestion",
  "input": {
    "question": "你说的王总是 XX 公司那个王晓峰，还是 YY 集团的王伟民？",
    "options": [
      {"label": "XX 公司王晓峰", "description": "市场部"},
      {"label": "YY 集团王伟民"}
    ],
    "multi": false,
    "allow_custom": true
  }
}
```

| input 字段 | 类型 | 必填 | 说明 |
|-----------|------|------|------|
| `question` | string | 是 | 提示给用户的问题 |
| `options` | array | 否 | 预设选项数组。每项含 `label`（必填）和 `description`（可选） |
| `multi` | bool | 否 | 是否允许多选（仅在 `options` 非空时有意义）。默认 `false` |
| `allow_custom` | bool | 否 | 是否允许用户输入自定义文本。默认 `true` |

**客户端渲染建议**：
- 单选 + `allow_custom=true` → 选项按钮 + 一个"自定义答案"输入框
- 多选 + `allow_custom=true` → 多选框 + 自定义文本框
- 仅 `question`（无 options）→ 纯文本输入框
- `allow_custom=false` → 严格只能选预设选项

**回传**：用户提交答案后，客户端通过标准 `tool.result` 消息回传，`output` 字段是用户最终选择/输入的文本。多选时建议用换行或分号分隔。例如：

```json
{
  "type": "tool.result",
  "tool_use_id": "toolu_ask_001",
  "status": "success",
  "output": "XX 公司王晓峰"
}
```

如果用户取消或关闭对话，回传 `status: "cancelled"` 即可，emma 会理解为放弃追问并继续。

#### 6.4.2 服务端工具执行

当服务端配置为 server-side 工具执行模式（`client_tools: false`）时，工具由服务端直接执行。客户端通过 `tool.start` 和 `tool.end` 事件感知执行过程。

#### `tool.start` — 服务端工具执行开始

服务端开始执行工具时推送，携带工具名称和输入参数。

```json
{
  "type": "tool.start",
  "event_id": "evt_060",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_001",
  "tool_name": "bash",
  "input": {
    "command": "ls -la"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `tool_use_id` | string | 工具调用唯一 ID（对应 LLM 输出 `tool_use` 内容块的 `id`） |
| `tool_name` | string | 工具名称 |
| `input` | object | 工具输入参数（JSON 对象，从 LLM 输出解析） |

#### `tool.end` — 服务端工具执行完成

服务端工具执行结束后推送，携带执行结果和状态。

```json
{
  "type": "tool.end",
  "event_id": "evt_061",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_001",
  "tool_name": "bash",
  "status": "success",
  "output": "total 48\ndrwxr-xr-x  12 user staff  384 Apr  7 10:00 .\n...",
  "is_error": false,
  "duration_ms": 50,
  "render_hint": "terminal",
  "metadata": {
    "exit_code": 0,
    "command": "ls -la"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `tool_use_id` | string | 工具调用唯一 ID |
| `tool_name` | string | 工具名称 |
| `status` | string | `"success"` 或 `"error"` |
| `output` | string | 工具执行输出内容 |
| `is_error` | bool | 是否为错误（命令执行失败等） |
| `duration_ms` | int64 | 执行耗时 (ms)，可选 |
| `render_hint` | string | 渲染提示，引导客户端选择渲染组件（见 §6.4.4 Render Hints），可选 |
| `language` | string | 语法高亮语言 ID（如 `"go"`、`"python"`），用于 `code`/`diff`/`file_info` 类型，可选 |
| `file_path` | string | 关联文件路径，用于 `code`/`diff`/`file_info` 类型，可选 |
| `artifacts` | array | **v1.13 新增**：本次工具调用产出的 artifact 列表（[]ArtifactRef，见 §10.6）。出现条件：(1) `ArtifactWrite` 单条产出；(2) `Specialists` / `Task` 工具的聚合产出（含 sub-agent 提交的全部 artifact）。空数组省略 |
| `metadata` | object | 工具特定的元数据，可选（如 `exit_code`、`command`） |

**`status` 与 `is_error` 的区别**:
- `status` 表示执行层面的状态：成功完成为 `"success"`，出错为 `"error"`
- `is_error` 表示工具结果是否应作为错误传递给 LLM（与 Anthropic API `tool_result.is_error` 对齐）
- 通常两者一致，但某些场景下 `status: "success"` 且 `is_error: true`（如命令返回非零 exit code 但执行本身未出错）

#### 6.4.4 Render Hints

`tool.end` 的 `render_hint` 字段为客户端提供语义信息，用于选择合适的渲染组件。工具在 `ToolResult.Metadata` 中设置 `render_hint`、`language`、`file_path` 三个 key，mapper 将它们提升为 `tool.end` 的 top-level 字段，并从残余 `metadata` 中移除（与 `duration_ms` 模式一致）。

**render_hint 枚举**:

| render_hint | 适用工具 | 客户端渲染建议 |
|-------------|---------|--------------|
| `terminal` | Bash | 终端/Shell 样式渲染，等宽字体，支持 ANSI 颜色 |
| `code` | Read | 代码查看器，配合 `language` 做语法高亮，配合 `file_path` 显示文件路径 |
| `diff` | Edit | Diff/Patch 渲染器，配合 `language` 和 `file_path` |
| `file_info` | Write | 文件信息卡片，显示创建/覆写状态 |
| `search` | Grep, Glob, WebSearch, TavilySearch | 搜索结果列表，高亮匹配项 |
| `markdown` | WebFetch | Markdown 渲染器 |
| `agent` | Task / Specialists | 子 Agent 输出摘要（v1.13 起 `artifacts` 字段携带聚合 Refs） |
| `artifact` | ArtifactWrite（v1.13+） | "已产出" 卡片：用 ArtifactRef.name 当标题 + description 副标题 + 可选 preview 折叠展开 |
| `artifact_view` | ArtifactRead（v1.13+） | "查看" 视图：mode=preview 时高亮展示 preview，mode=full 时按 mime_type 选择渲染器 |
| `task_submission` | SubmitTaskResult（v1.13+） | 提交结果：成功 → 绿色"任务完成"卡片含产物列表；失败（is_error=true）→ 警示卡含 `reason` |
| `skill` | Skill | Skill 执行结果 |
| `task` | TaskCreate, TaskGet, TaskUpdate, TaskList | 任务卡片/列表 |
| `message` | SendMessage | 消息气泡 |
| `team` | TeamCreate, TeamDelete | 团队操作结果 |
| `plain` | 默认 | 纯文本展示 |

**辅助字段**:

| 字段 | 说明 | 常见 render_hint |
|------|------|-----------------|
| `language` | 语法高亮语言 ID（如 `"go"`、`"python"`、`"typescript"`）。由 `ExtToLanguage()` 从文件扩展名推导 | `code`, `diff`, `file_info` |
| `file_path` | 关联的文件路径 | `code`, `diff`, `file_info` |

**客户端处理建议**:
- 根据 `render_hint` 选择渲染组件
- 未识别的 `render_hint` 值应 fallback 到纯文本渲染
- `render_hint` 缺失（空字符串）时使用纯文本渲染
- `language` 和 `file_path` 仅在 `render_hint` 为 `code`/`diff`/`file_info` 时有意义

**tool.end 示例（Read 工具）**:

```json
{
  "type": "tool.end",
  "event_id": "evt_062",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_002",
  "tool_name": "Read",
  "status": "success",
  "output": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}",
  "is_error": false,
  "duration_ms": 2,
  "render_hint": "code",
  "language": "go",
  "file_path": "/src/main.go",
  "metadata": {
    "start_line": 1,
    "lines_read": 7
  }
}
```

**tool.end 示例（ArtifactWrite，v1.13+）**:

```json
{
  "type": "tool.end",
  "event_id": "evt_063",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_003",
  "tool_name": "ArtifactWrite",
  "status": "success",
  "output": "{\"artifact_id\":\"art_a1b2c3d4...\",\"uri\":\"artifact://art_a1b2c3d4...\",\"size_bytes\":1240,\"version\":1,\"ref\":{...}}",
  "is_error": false,
  "duration_ms": 8,
  "render_hint": "artifact",
  "artifacts": [
    {
      "artifact_id": "art_a1b2c3d4e5f6789012345678",
      "name": "intern-schedule-email.md",
      "type": "file",
      "mime_type": "text/markdown",
      "size_bytes": 1240,
      "description": "实习生作息安排邮件正稿",
      "preview_text": "尊敬的张老师，关于本期实习生的工作作息..."
    }
  ]
}
```

**tool.end 示例（Specialists 聚合产出，v1.13+）**:

emma 调 Specialists 完成时，`artifacts` 字段聚合 sub-agent 在本次执行中通过 SubmitTaskResult 提交的全部 artifact。这是前端"本次用户请求最终产出了什么"的 trace 级锚点。

```json
{
  "type": "tool.end",
  "event_id": "evt_064",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_004",
  "tool_name": "Specialists",
  "status": "success",
  "is_error": false,
  "duration_ms": 12500,
  "render_hint": "agent",
  "artifacts": [
    {
      "artifact_id": "art_a1b2c3d4...",
      "name": "intern-schedule-email.md",
      "type": "file",
      "size_bytes": 1240,
      "description": "实习生作息安排邮件正稿",
      "role": "draft_email"
    }
  ],
  "metadata": {
    "agent_id": "sess_xxx_sub_yyy",
    "status": "completed",
    "num_turns": 6,
    "input_tokens": 8500,
    "output_tokens": 1240
  }
}
```

**tool.end 示例（SubmitTaskResult，v1.13+）**:

L3 sub-agent 任务完成时调用，`artifacts` 透传 SubmitTaskResult 的入参（已通过 M4 校验）。

```json
{
  "type": "tool.end",
  "event_id": "evt_065",
  "session_id": "sess_abc123",
  "tool_use_id": "toolu_005",
  "tool_name": "SubmitTaskResult",
  "status": "success",
  "is_error": false,
  "duration_ms": 3,
  "render_hint": "task_submission",
  "output": "{\"status\":\"accepted\",\"summary\":\"邮件已生成\",\"artifacts\":[...]}",
  "artifacts": [
    {
      "artifact_id": "art_a1b2c3d4...",
      "name": "intern-schedule-email.md",
      "type": "file",
      "role": "draft_email",
      "size_bytes": 1240
    }
  ]
}
```

**tool.end 示例（SubmitTaskResult 被拒，v1.13+）**:

```json
{
  "type": "tool.end",
  "event_id": "evt_066",
  "tool_name": "SubmitTaskResult",
  "status": "error",
  "is_error": true,
  "render_hint": "task_submission",
  "output": "Submission rejected: ...\n- artifacts[0] (id=art_xxx, role=report): role \"report\" is not in the contract; valid roles: [draft_email]\n",
  "metadata": {
    "submission_accepted": false,
    "reason": "..."
  }
}
```

注意 `is_error=true` + `render_hint=task_submission` 是"L3 提交被框架打回"的关键信号，前端宜用警告样式（黄/红），LLM 看到此结果会在下一轮修正再交。

#### 6.4.3 权限审批

当服务端工具执行模式下，权限检查结果为 `Ask`（如写操作在 default/plan 模式下需要用户确认）时，服务端不直接拒绝，而是通过 `permission.request` 事件询问客户端。客户端回复 `permission.response`（§5.3）后，服务端根据审批结果继续或中止执行。

若用户此前已选择「会话级允许」（`scope: "session"`），则同一会话内相同工具的后续调用将跳过审批、自动通过。

#### `permission.request` — 请求权限审批

```json
{
  "type": "permission.request",
  "event_id": "evt_070",
  "session_id": "sess_abc123",
  "request_id": "perm_a1b2c3d4",
  "tool_name": "Bash",
  "tool_input": "{\"command\":\"rm -rf ./build\"}",
  "message": "Allow rm to make changes?",
  "is_read_only": false,
  "permission_key": "Bash:rm",
  "options": [
    {"label": "Allow once",                          "scope": "once",    "allow": true},
    {"label": "Always allow rm in this session",     "scope": "session", "allow": true},
    {"label": "Deny",                                "scope": "once",    "allow": false}
  ]
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `request_id` | string | 审批请求唯一 ID（`perm_` 前缀），客户端回复时必须携带 |
| `tool_name` | string | 需要审批的工具名称 |
| `tool_input` | string | 工具输入参数（JSON 字符串） |
| `message` | string | 人类可读的审批提示（如 "Allow rm to make changes?"） |
| `is_read_only` | bool | 工具是否为只读操作 |
| `permission_key` | string | 会话级审批的细粒度 key。Bash 工具为 `Bash:<程序名 子命令>`（如 `Bash:git status`、`Bash:npm install`）；无子命令时为 `Bash:<程序名>`（如 `Bash:rm`）；文件工具为 `<工具>:<路径>`（如 `Edit:/src/main.go`）；其它工具为工具名本身 |
| `options` | array | 客户端应展示的操作选项列表 |

`options` 数组中的每个元素：

| 字段 | 类型 | 说明 |
|------|------|------|
| `label` | string | 按钮/选项的显示文本（会包含具体命令，如 "Always allow git status in this session"） |
| `scope` | string | `"once"` 单次生效、`"session"` 会话级生效 |
| `allow` | bool | `true` 表示批准、`false` 表示拒绝 |

**`permission_key` 提取规则**：

| 工具 | 输入示例 | permission_key | 说明 |
|------|---------|----------------|------|
| Bash | `{"command":"git status"}` | `Bash:git status` | 程序名 + 子命令 |
| Bash | `{"command":"git push --force"}` | `Bash:git push` | 跳过 flags，保留子命令 |
| Bash | `{"command":"git add file.go"}` | `Bash:git add` | 文件参数不算子命令 |
| Bash | `{"command":"sudo npm install foo"}` | `Bash:npm install` | 跳过 sudo，取程序名 + 子命令 |
| Bash | `{"command":"docker compose up -d"}` | `Bash:docker compose` | 多级子命令取前两级 |
| Bash | `{"command":"rm -rf /tmp"}` | `Bash:rm` | 无子命令，仅程序名 |
| Bash | `{"command":"ls -la"}` | `Bash:ls` | flags 不算子命令 |
| Bash | `{"command":"ENV=1 go build ./..."}` | `Bash:go build` | 跳过环境变量赋值 |
| Edit | `{"file_path":"/src/main.go",...}` | `Edit:/src/main.go` | 工具名 + 文件路径 |
| Write | `{"file_path":"/tmp/out.txt",...}` | `Write:/tmp/out.txt` | 工具名 + 文件路径 |
| Grep | `{"pattern":"TODO",...}` | `Grep` | 只读工具，直接用工具名 |

**会话级审批的粒度是 `permission_key`（程序 + 子命令），而非程序名或工具名**。用户选择「会话级允许 git status」后，只有 `Bash:git status` 的后续调用会自动审批；`Bash:git push`、`Bash:git add` 等仍需单独确认。

**客户端收到 `permission.request` 后 MUST**：
1. 向用户展示审批请求（工具名称、输入参数、审批提示）
2. 根据 `options` 渲染操作按钮（通常为三个：单次允许、会话级允许、拒绝）
3. 收集用户的选择
4. 通过 `permission.response`（§5.3）回传审批结果，`scope` 字段与用户所选 option 的 `scope` 一致

**审批窗口**: 无超时限制。服务端将一直等待直到用户操作或会话中断。

**会话级自动审批**: 若此前用户已选择 `scope: "session"` 批准了某工具，则后续对同一工具的权限检查将跳过 `permission.request`，服务端直接视为已批准。

**时序关系**: `permission.request` 出现在 `tool.start` 之前（因为工具尚未开始执行）。审批通过后服务端才发送 `tool.start` 并开始实际执行。

### 6.5 Task Lifecycle

#### `task.end` — query-loop 任务结束

query-loop 结束后推送（所有 `message.stop` 之后），汇总整轮执行情况。

```json
{
  "type": "task.end",
  "event_id": "evt_030",
  "session_id": "sess_abc123",
  "request_id": "evt_client_001",
  "status": "success",
  "duration_ms": 3200,
  "num_turns": 2,
  "total_usage": {
    "input_tokens": 350,
    "output_tokens": 120,
    "cache_read_tokens": 100,
    "cache_write_tokens": 0
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `request_id` | string | 对应触发请求的客户端 `event_id` |
| `status` | string | 完成状态（见下表） |
| `duration_ms` | int64 | query-loop 总耗时 (ms) |
| `num_turns` | int | LLM 调用轮次数 |
| `total_usage` | object | query-loop 累计 token 用量 |

**status 枚举**:

| status | 说明 |
|--------|------|
| `success` | 正常完成（最后一次 LLM 调用 stop_reason 为 `end_turn`） |
| `error_max_turns` | 达到最大轮次限制 |
| `error_model` | LLM API 不可恢复错误 |
| `aborted` | 用户中断（streaming 或 tools 阶段） |
| `error` | 其他执行错误（prompt_too_long, blocking_limit 等） |

### 6.6 Sub-Agent Events

当主 Agent 通过 Agent 工具生成同步子 Agent 时，服务端会在子 Agent 执行前后各推送一个事件，客户端据此显示加载指示器。

#### `subagent.start` — 子 Agent 开始执行

```json
{
  "type": "subagent.start",
  "event_id": "evt_a1b2c3d4",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "researcher",
  "description": "调研 LLM 推理",
  "task": "调研大模型推理优化的最新进展，重点关注 vLLM、SGLang、KV-cache 优化方向，整理一份给老板的简报",
  "agent_type": "sync",
  "parent_agent_id": "main"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 子 Agent 唯一 ID |
| `agent_name` | string | Agent 标识名（由 LLM 在 Agent 工具 `name` 参数中指定，可选） |
| `description` | string | 3-5 字短标签（由派发者在 Agent 工具 `description` 参数中指定） |
| `task` | string | **v1.12 新增**：父 Agent 派给子 Agent 的完整任务 prompt（按 rune 截断到 800 字）。让用户启动瞬间就看到子 Agent 接到的实际任务文本，弥补 `description` 信息量过低的缺陷。**向前兼容**：旧客户端忽略此字段即可 |
| `agent_type` | string | Agent 类型。Phase 1 固定为 `"sync"` |
| `parent_agent_id` | string | 父 Agent ID。顶层 query-loop 为 `"main"` |

#### `subagent.end` — 子 Agent 执行完成

```json
{
  "type": "subagent.end",
  "event_id": "evt_i9j0k1l2",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "writer",
  "status": "completed",
  "duration_ms": 12300,
  "num_turns": 3,
  "usage": {
    "input_tokens": 15000,
    "output_tokens": 3200,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0
  },
  "denied_tools": [],
  "artifacts": [
    {
      "artifact_id": "art_a1b2c3d4e5f6789012345678",
      "name": "intern-schedule-email.md",
      "type": "file",
      "mime_type": "text/markdown",
      "size_bytes": 1240,
      "description": "实习生作息安排邮件正稿",
      "role": "draft_email"
    }
  ]
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 对应 `subagent.start` 中的 `agent_id` |
| `agent_name` | string | Agent 标识名 |
| `status` | string | 执行结果状态（见下表） |
| `duration_ms` | int64 | 执行总耗时（毫秒） |
| `num_turns` | int | LLM 调用轮次数 |
| `usage` | object | 子 Agent 累计 token 用量 |
| `denied_tools` | array | 子 Agent 尝试调用但因权限被拒绝的工具名列表 |
| `artifacts` | array | **v1.13 新增**：本次 sub-agent 任务的全部产物（[]ArtifactRef，见 §10.6）。来源优先级：(1) 任务带 `expected_outputs` 契约时取 SubmitTaskResult 校验通过的列表（含 role 字段）；(2) 否则取 sub-agent 内全部 ArtifactWrite 的合集。空数组省略 |

**status 枚举**:

| status | 说明 |
|--------|------|
| `completed` | 正常完成（LLM stop_reason 为 `end_turn`；契约模式下还需 SubmitTaskResult 通过 M4 校验） |
| `max_turns` | 达到最大轮次限制 —— 含两种情形：(1) LLM 真的跑完最大轮次；(2) **v1.13** 契约模式下 SubmitTaskResult 被 nudge ≥3 次仍未交 / 校验拒 ≥3 次。客户端可读 `subagent.end.terminal.message` 区分 |
| `model_error` | LLM API 不可恢复错误 |
| `aborted` | 父 Agent 或用户取消 |
| `timeout` | 执行超时 |

**客户端处理建议**:
- 收到 `subagent.start` 时显示 spinner + Agent 描述（如 "Searching authentication module..."）
- 收到 `subagent.event` 时实时渲染子 Agent 的文本输出和工具执行状态（见下文）
- 收到 `subagent.end` 时关闭 spinner，显示耗时和状态
- `denied_tools` 非空时，可向用户展示权限提示

**注意事项**:
- 子 Agent 的内部事件通过 `subagent.event` 包装后实时转发给客户端（v1.8 起），客户端可渐进渲染子 Agent 的文本输出和工具执行过程
- 子 Agent 的文本输出同时作为父 Agent 的 Agent 工具结果返回，会在后续父 Agent 的 `content.delta` 中体现
- 同一时刻可能有多个子 Agent 并行执行（Phase 4），通过 `agent_id` 区分

#### `subagent.event` — 子 Agent 实时流式事件

子 Agent 执行过程中，服务端将其**工具执行事件**与**任务进度信号**实时包装为 `subagent.event` 转发给客户端。此事件嵌套在 `subagent.start` 和 `subagent.end` 之间。

> **v1.12 新增**: `event_type: "intent"` ——每次工具调用前的任务进度句（如"researcher 正在搜索 vLLM 论文"）。由框架强制注入的 `intent` 必填字段保证：模型必须告知"这次调用要做什么"。详见 §6.6.1。
>
> **v1.10（Breaking）**: 子 Agent 的 LLM 文本输出（原 `event_type: "text"`）**不再**通过此事件转发。所有用户可见的对话文本现在仅来自 L1 主体的 `content.delta` 流——L1 会读取子 Agent 的 `<summary>` 后用自己的口吻润色再回复用户。
>
> 当前 `event_type` 取值（v1.12）: `"intent"` / `"tool_start"` / `"tool_end"`。

**任务进度（v1.12）**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_001",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "researcher",
  "payload": {
    "event_type": "intent",
    "tool_name": "WebSearch",
    "tool_use_id": "toolu_sub_1",
    "intent": "正在搜索 vLLM 推理优化的最新论文"
  }
}
```

`intent` 帧总是先于同 `tool_use_id` 的 `tool_start` 到达，客户端可以在 spinner 旁渲染进度句。

**工具开始执行**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_002",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "researcher",
  "payload": {
    "event_type": "tool_start",
    "tool_name": "WebSearch",
    "tool_use_id": "toolu_sub_1",
    "tool_input": "{\"query\":\"vLLM inference optimization 2026\"}"
  }
}
```

注意 `tool_input` **不再包含 `intent` 字段**——框架在执行前已剥离，让真实工具的输入保持干净。

**工具执行完成**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_003",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "researcher",
  "payload": {
    "event_type": "tool_end",
    "tool_name": "WebSearch",
    "tool_use_id": "toolu_sub_1",
    "output": "found 8 results: ...",
    "is_error": false
  }
}
```

**工具执行完成（ArtifactWrite，v1.13+）**:

L3 / L2 sub-agent 调 ArtifactWrite 后，框架在转发的 `tool_end` payload 上附 `artifacts` 字段（[]ArtifactRef，见 §10.6），客户端能在 sub-agent 干活的过程中**实时点亮单个 artifact 卡**，不必等到 subagent.end 才看到。

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_004",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "writer",
  "payload": {
    "event_type": "tool_end",
    "tool_name": "ArtifactWrite",
    "tool_use_id": "toolu_sub_2",
    "output": "{\"artifact_id\":\"art_a1b2c3d4...\",\"size_bytes\":1240,...}",
    "is_error": false,
    "artifacts": [
      {
        "artifact_id": "art_a1b2c3d4e5f6789012345678",
        "name": "intern-schedule-email.md",
        "type": "file",
        "mime_type": "text/markdown",
        "size_bytes": 1240,
        "description": "实习生作息安排邮件正稿"
      }
    ]
  }
}
```

**SubmitTaskResult 通过 / 被拒（v1.13+）**:

L3 调 SubmitTaskResult 时，`payload.artifacts` 携带提交清单（通过校验时含 role 字段；被拒时为 nil 或空）。`is_error` 反映校验结果，前端可据此选择成功或警告样式：

```json
{
  "type": "subagent.event",
  "payload": {
    "event_type": "tool_end",
    "tool_name": "SubmitTaskResult",
    "tool_use_id": "toolu_sub_3",
    "is_error": false,
    "output": "{\"status\":\"accepted\",\"summary\":\"邮件已生成\",...}",
    "artifacts": [
      {"artifact_id": "art_a1b2c3d4...", "name": "intern-schedule-email.md", "role": "draft_email", ...}
    ]
  }
}
```

被拒时：

```json
{
  "type": "subagent.event",
  "payload": {
    "event_type": "tool_end",
    "tool_name": "SubmitTaskResult",
    "is_error": true,
    "output": "Submission rejected:\n- artifacts[0] (id=art_xxx, role=report): role \"report\" is not in the contract..."
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 对应 `subagent.start` 中的 `agent_id` |
| `agent_name` | string | Agent 标识名 |
| `payload` | object | 子 Agent 内部事件数据（见下表） |

**payload 字段**:

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `event_type` | string | 是 | 内部事件类型：`"intent"`（v1.12+）、`"tool_start"`、`"tool_end"`。v1.10 起 `"text"` 已废弃 |
| `text` | string | 否 | **已废弃**（v1.10）。服务端不再发送此字段 |
| `tool_name` | string | 否 | 工具名（`intent` / `tool_start` / `tool_end` 均带） |
| `tool_use_id` | string | 否 | 工具调用 ID（`intent` / `tool_start` / `tool_end` 均带，用于关联同一次调用） |
| `tool_input` | string | 否 | 工具输入 JSON 字符串（`tool_start` 时）。**不含框架剥离的 `intent` 字段** |
| `intent` | string | 否 | **v1.12 新增**：模型在调用前提供的进度句（`event_type: "intent"` 时） |
| `output` | string | 否 | 工具输出内容（`tool_end` 时） |
| `is_error` | bool | 否 | 工具执行是否出错（`tool_end` 时） |
| `artifacts` | array | 否 | **v1.13 新增**：当 `event_type: "tool_end"` 且 sub-agent 工具产出了 artifact 时附带（[]ArtifactRef，见 §10.6）。出现于 `ArtifactWrite` 单次产出 / `SubmitTaskResult` 通过校验时；其它工具 / 被拒时为空 |

**客户端处理建议**:
- `event_type: "intent"` — 用 `tool_use_id` 关联同一次调用，把 `intent` 文本作为该工具调用的标题显示（"researcher 正在搜 vLLM 论文"）
- `event_type: "tool_start"` — 显示工具执行 spinner，可选展示 `tool_input` 摘要
- `event_type: "tool_end"` — 更新工具状态为完成/失败，可选展示输出摘要
- 使用 `agent_id` 将事件关联到对应的子 Agent UI 组件
- 当某次工具调用**没有**先来一条 `intent` 帧时（模型未填或填空），客户端应回落到工具名 + 简略输入的渲染——不要假设 intent 必到
- 用户可见的对话文本应**仅**来自 L1 的 `content.delta` 流，子 Agent 文本由 L1 润色后再呈现

#### 6.6.1 `agent.intent` — 主 Agent 工具进度（v1.12+）

主 Agent（emma 等顶层 query-loop）调用工具时，框架在 `tool.start` 之前推送此独立事件，与 sub-agent 的 `subagent.event{intent}` 形成对称。

```json
{
  "type": "agent.intent",
  "event_id": "evt_int_001",
  "session_id": "sess_abc123",
  "agent_id": "",
  "agent_name": "emma",
  "tool_use_id": "toolu_main_1",
  "tool_name": "WebSearch",
  "intent": "查一下王总最近的会议时间"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 主 Agent 通常为空字符串；保留字段为多 main-agent 拓扑预留 |
| `agent_name` | string | 主 Agent 标识名（默认 `"emma"`） |
| `tool_use_id` | string | 工具调用 ID，对应紧随其后的 `tool.start` / `tool.end` |
| `tool_name` | string | 工具名 |
| `intent` | string | 模型在 `intent` 字段中提供的进度句 |

**强约束机制**:
- 框架在 `ToolPool.Schemas()` 输出每个工具的 `InputSchema` 时**注入 `intent: string` 必填字段**——LLM provider 的 schema 验证层把"必须填"硬挡住，模型不可能漏报
- `ToolExecutor.executeSingle` 在执行前从 input 抽出 `intent`，发本事件，然后**剥掉字段**再交给真实工具——所以工具实现不需要任何修改，旧工具自动获得任务进度可视化
- 如果工具自身已经在 `InputSchema` 中声明了 `intent` 字段（自定义语义），框架尊重并跳过注入

**优雅降级**:
- provider 偶尔放宽 schema 校验，模型缺 intent / 填空字符串 / 填非字符串值时，框架不发 `agent.intent` / `subagent.event{intent}`，但工具照常执行——用户漏看一句进度好过整条链路阻塞

**客户端处理建议**:
- 用 `tool_use_id` 把 `agent.intent` 与紧随其后的 `tool.start` 关联，显示为"emma 正在查王总会议时间"这样的可读标题
- 如果某次 `tool.start` 没有先来 `agent.intent`，客户端应回落到工具名 + 简略输入的渲染
- 与 sub-agent 的 intent 事件**结构对称**：emma 用 `agent.intent`（顶层事件），researcher 等用 `subagent.event{event_type=intent}`（包装事件）。前端可统一抽象成"agent X intent: Y"

### 6.7 @-Mention Routing Event

当用户消息中包含 @-mention 路由到特定 Agent 时，服务端推送此事件通知客户端。

#### `agent.routed` — @-mention 路由通知

```json
{
  "type": "agent.routed",
  "event_id": "evt_r1s2t3u4",
  "session_id": "sess_abc123",
  "agent_id": "agent_abc123",
  "agent_name": "backend-dev",
  "description": "Handling backend implementation",
  "agent_type": "general-purpose"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 被路由到的 Agent ID |
| `agent_name` | string | Agent 标识名 |
| `description` | string | Agent 描述 |
| `agent_type` | string | Agent 类型 |

**客户端处理建议**:
- 显示路由指示器（如 "Routed to @backend-dev"）
- 后续事件可能来自该 Agent 的执行上下文

### 6.8 Task Events

任务系统事件，通知客户端任务的创建和状态变更。需要 `capabilities.tasks: true`。

#### `task.created` — 任务创建

```json
{
  "type": "task.created",
  "event_id": "evt_m3n4o5p6",
  "session_id": "sess_main",
  "task": {
    "task_id": "1",
    "subject": "Implement user authentication API",
    "status": "pending",
    "scope_id": "team_abc"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `task.task_id` | string | 任务唯一 ID |
| `task.subject` | string | 任务标题 |
| `task.status` | string | 初始状态，固定为 `"pending"` |
| `task.scope_id` | string | 作用域 ID（session ID 或 team ID） |

#### `task.updated` — 任务状态变更

```json
{
  "type": "task.updated",
  "event_id": "evt_q7r8s9t0",
  "session_id": "sess_main",
  "task": {
    "task_id": "1",
    "subject": "Implement user authentication API",
    "status": "in_progress",
    "owner": "backend-dev",
    "active_form": "Implementing authentication API",
    "scope_id": "team_abc"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `task.task_id` | string | 任务唯一 ID |
| `task.subject` | string | 任务标题 |
| `task.status` | string | 任务状态：`"pending"` / `"in_progress"` / `"completed"` / `"deleted"` |
| `task.owner` | string | 任务所有者（Agent 名称），可选 |
| `task.active_form` | string | 进行中的动词形式（如 "Running tests"），用于 spinner 显示，可选 |
| `task.scope_id` | string | 作用域 ID |

**客户端处理建议**:
- `task.created`: 在任务列表中新增待办项
- `task.updated` + `status: "in_progress"`: 显示 spinner + `active_form` 文案
- `task.updated` + `status: "completed"`: 任务打钩
- `task.updated` + `status: "deleted"`: 移除任务项

### 6.9 Agent Message Event

Agent 间通信事件，客户端据此展示 Agent 协作过程。需要 `capabilities.messaging: true`。

#### `agent.message` — Agent 间消息

```json
{
  "type": "agent.message",
  "event_id": "evt_u1v2w3x4",
  "session_id": "sess_main",
  "message": {
    "from": "backend-dev",
    "to": "coordinator",
    "summary": "Authentication API implementation complete, 3 endpoints created...",
    "team_id": "team_abc"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `message.from` | string | 发送者 Agent 名称 |
| `message.to` | string | 接收者 Agent 名称（`"*"` 表示广播） |
| `message.summary` | string | 消息摘要 |
| `message.team_id` | string | 所属团队 ID，可选 |

**客户端处理建议**:
- 在时间线或消息面板中显示消息气泡
- 标注发送者和接收者

### 6.10 Async Agent Events

异步 Agent 生命周期事件。需要 `capabilities.async_agent: true`。

#### `agent.spawned` — 异步 Agent 启动

```json
{
  "type": "agent.spawned",
  "event_id": "evt_sp1",
  "session_id": "sess_main",
  "agent_id": "agent_c9d0e1f2",
  "agent_name": "data-analyzer",
  "description": "Analyzing sales data",
  "agent_type": "async",
  "parent_agent_id": "coordinator"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | Agent 唯一 ID |
| `agent_name` | string | Agent 标识名 |
| `description` | string | Agent 描述 |
| `agent_type` | string | 固定为 `"async"` |
| `parent_agent_id` | string | 父 Agent ID |

#### `agent.idle` — Agent 进入空闲

```json
{
  "type": "agent.idle",
  "event_id": "evt_id1",
  "session_id": "sess_main",
  "agent_id": "agent_c9d0e1f2",
  "agent_name": "data-analyzer"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | Agent 唯一 ID |
| `agent_name` | string | Agent 标识名 |

#### `agent.completed` — 异步 Agent 完成

```json
{
  "type": "agent.completed",
  "event_id": "evt_cp1",
  "session_id": "sess_main",
  "agent_id": "agent_c9d0e1f2",
  "agent_name": "data-analyzer",
  "status": "completed",
  "duration_ms": 45000,
  "usage": {
    "input_tokens": 50000,
    "output_tokens": 12000,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | Agent 唯一 ID |
| `agent_name` | string | Agent 标识名 |
| `status` | string | 固定为 `"completed"` |
| `duration_ms` | int64 | 执行总耗时（毫秒） |
| `usage` | object | 累计 token 用量 |

#### `agent.failed` — 异步 Agent 失败

```json
{
  "type": "agent.failed",
  "event_id": "evt_fl1",
  "session_id": "sess_main",
  "agent_id": "agent_c9d0e1f2",
  "agent_name": "data-analyzer",
  "error": {
    "type": "timeout",
    "message": "Agent execution exceeded 5 minute timeout"
  },
  "duration_ms": 300000
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | Agent 唯一 ID |
| `agent_name` | string | Agent 标识名 |
| `error` | object | 错误信息 |
| `error.type` | string | 错误类型（`"timeout"` / `"model_error"` / `"internal_error"`） |
| `error.message` | string | 人类可读错误描述 |
| `duration_ms` | int64 | 执行总耗时（毫秒） |

**客户端处理建议**:
- `agent.spawned`: 新增 Agent 卡片（活跃态）
- `agent.idle`: Agent 卡片变为等待态（灰色）
- `agent.completed`: Agent 卡片标记完成（打钩）
- `agent.failed`: Agent 卡片标记失败（红色 + 错误信息）

### 6.11 Team Events

团队生命周期事件。需要 `capabilities.teams: true`。

#### `team.created` — 团队创建

```json
{
  "type": "team.created",
  "event_id": "evt_tc1",
  "session_id": "sess_main",
  "team": {
    "team_id": "team_abc",
    "team_name": "fullstack-feature",
    "members": ["coordinator"]
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `team.team_id` | string | 团队唯一 ID |
| `team.team_name` | string | 团队名称 |
| `team.members` | array | 初始成员列表 |

#### `team.member_join` — 成员加入

```json
{
  "type": "team.member_join",
  "event_id": "evt_tj1",
  "session_id": "sess_main",
  "team": {
    "team_id": "team_abc",
    "member_name": "frontend-dev",
    "member_type": "teammate",
    "members": ["coordinator", "frontend-dev", "backend-dev"]
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `team.team_id` | string | 团队 ID |
| `team.member_name` | string | 加入的成员名称 |
| `team.member_type` | string | 成员类型（如 `"teammate"`、`"coordinator"`） |
| `team.members` | array | 加入后的完整成员列表 |

#### `team.member_left` — 成员离开

```json
{
  "type": "team.member_left",
  "event_id": "evt_tl1",
  "session_id": "sess_main",
  "team": {
    "team_id": "team_abc",
    "member_name": "frontend-dev",
    "members": ["coordinator", "backend-dev"]
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `team.team_id` | string | 团队 ID |
| `team.member_name` | string | 离开的成员名称 |
| `team.members` | array | 离开后的完整成员列表 |

#### `team.deleted` — 团队解散

```json
{
  "type": "team.deleted",
  "event_id": "evt_td1",
  "session_id": "sess_main",
  "team": {
    "team_id": "team_abc",
    "team_name": "fullstack-feature"
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `team.team_id` | string | 团队 ID |
| `team.team_name` | string | 团队名称 |

**客户端处理建议**:
- `team.created`: 显示团队面板，列出初始成员
- `team.member_join`: 团队面板新增成员
- `team.member_left`: 团队面板移除成员
- `team.deleted`: 团队面板收起或消失

### 6.12 Error Event

#### `error` — 错误通知

```json
{
  "type": "error",
  "event_id": "evt_040",
  "session_id": "sess_abc123",
  "request_id": "evt_client_001",
  "error": {
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "message": "Too many requests. Please retry after 30 seconds.",
    "retry_after_ms": 30000
  }
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `request_id` | string | 触发错误的客户端 `event_id`（如可关联） |
| `error.type` | string | 错误分类（见下表） |
| `error.code` | string | 机器可读错误码 |
| `error.message` | string | 人类可读描述 |
| `error.retry_after_ms` | int | 建议重试等待时间（可选） |

**error.type 枚举**:

| error.type | HTTP 等价 | 说明 | 客户端处理建议 |
|------------|-----------|------|---------------|
| `authentication_error` | 401 | 认证失败 | 检查 API Key，不重试 |
| `permission_error` | 403 | 权限不足 | 检查权限配置，不重试 |
| `not_found_error` | 404 | 资源不存在（如 session） | 创建新 session |
| `rate_limit_error` | 429 | 请求频率超限 | 按 `retry_after_ms` 等待后重试 |
| `invalid_request_error` | 400 | 请求参数错误 | 修正请求，不自动重试 |
| `overloaded_error` | 529 | 服务过载 | 指数退避重试 |
| `internal_error` | 500 | 服务端内部错误 | 指数退避重试 |

**错误是否关闭连接**:
- 大多数错误是可恢复的（recoverable），连接保持打开
- `authentication_error` 后服务端 **MAY** 关闭连接
- 连接级错误（如协议违规）直接发送 WebSocket Close frame

### 6.13 Emit Lifecycle Events (v1.11+)

v1.11 引入了一组结构化的生命周期事件，统一携带 **Envelope（信封）** + **Display（展示）** + **Metrics（指标）** 三段元数据。这些事件由框架在确定性的执行节点触发，让客户端可以拼出完整的多层执行树（trace → plan → step → tool）而无需解析任何 LLM 文本。

需要 `capabilities.emit: true`。所有事件向前兼容：旧客户端忽略不识别的 type 即可。

> **命名空间约定**: 编排步骤使用 **`step.*`** 前缀（不是 `task.*`）。`task.*` 命名空间属于 §6.8 的用户级 TodoList 系统，emit 的 plan-step 与之**完全分离**。前端做一级 `type` switch 时可以按 `step.` 前缀直接路由到 step 卡片渲染器。

#### 6.13.1 Envelope（公共元数据）

每个 emit 事件都携带 `envelope` 对象，所有字段由框架强制注入：

```json
"envelope": {
  "trace_id": "tr_a1b2c3...",
  "parent_event_id": "evt_xyz",
  "task_id": "s1",
  "parent_task_id": "plan_abc12345",
  "seq": 13,
  "timestamp": "2026-04-29T10:00:00.150Z",
  "agent_role": "orchestrator",
  "agent_id": "plan_agent",
  "agent_run_id": "run_l2_001",
  "severity": "info"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|:---:|------|
| `trace_id` | string | ✓ | 一次用户请求的根 ID，串起所有层级的事件 |
| `parent_event_id` | string | – | 直接因果父事件 ID（如 step.dispatched 的父是 plan.created） |
| `task_id` | string | – | 当前事件所属任务节点 ID（UI 上聚合到同一张卡片）；step 事件填 step_id，plan 事件填 plan_id |
| `parent_task_id` | string | – | 父任务 ID（step 的 parent_task_id 是 plan_id） |
| `seq` | int64 | ✓ | trace 内单调递增序列号，前端按 seq 排序解决乱序 |
| `timestamp` | ISO 8601 | ✓ | 事件 emit 时刻（UTC，毫秒精度） |
| `agent_role` | enum | ✓ | `"persona"` \| `"orchestrator"` \| `"worker"` \| `"system"` — 职责命名，详见下文 |
| `agent_id` | string | – | 具体 Agent 标识（`"emma"` / `"plan_agent"` / 子 Agent ID） |
| `agent_run_id` | string | – | Agent 本次运行实例 ID |
| `severity` | enum | ✓ | `"debug"` \| `"info"` \| `"warn"` \| `"error"` |

**`agent_role` 取值与映射**（v1.11 起用职责命名，**不暴露**内部 L1/L2/L3 层级抽象）：

| role | 当前实现 | 含义 |
|------|---------|------|
| `persona` | emma | 用户面 chat agent — 维护人格、与用户对话 |
| `orchestrator` | plan_agent | 编排 — 任务拆分、子 Agent 调度 |
| `worker` | sub-agent (search/analyst/...) | 执行 — 调工具、产出结果 |
| `system` | (无具体 agent) | 框架级事件（连接错误、心跳元信号等） |

> **关于 envelope 与顶层字段**: emit 事件**仍然**带顶层 `event_id` 和 `session_id`（§4 规则）。envelope 不重复二者，`agent_id` 仅在 envelope 内出现。详见 §4 "Emit 事件与顶层字段的关系"。

> **没有 `schema_version`**: emit 协议跟随 `protocol_version` 演进，不再单独维护版本号。客户端只看顶层 `protocol_version`。

> **使用场景**: 客户端可先按 envelope 路由（按 trace_id 分流到不同请求面板、按 seq 排序处理乱序、按 task_id 聚合卡片），再按 type 解析具体 payload。

#### 6.13.2 Display（UI 渲染）

`display` 是给前端看的字段，与业务 `payload` 解耦。**前端只消费 display 即可渲染卡片**，不需要理解 payload。

```json
"display": {
  "title": "派出 search_agent",
  "summary": "查 2024 销量",
  "icon": "dispatch",
  "visibility": "default"
}
```

| 字段 | 说明 |
|------|------|
| `title` | 卡片标题 |
| `summary` | 一句话说明 |
| `icon` | 图标语义键（受控枚举，见下表） |
| `visibility` | `"default"`（默认展开） \| `"collapsed"`（默认折叠） \| `"hidden"`（仅日志） |
| `persona_hint` | 给 persona（L1）的发声提示（可选） |

**`icon` 受控枚举**（v1.11 起）:

| icon | 典型场景 |
|------|---------|
| `plan` | 计划制定 / 计划完成 |
| `dispatch` | 派发任务给子 Agent |
| `search` | 信息检索（grep / web search） |
| `analysis` | 数据分析、对比 |
| `tool` | 通用工具调用 |
| `success` | 成功完成 |
| `error` | 失败 |
| `warning` | 警告 / 跳过 |
| `info` | 中性信息 |
| `agent` | 子 Agent 启动 / 完成 |
| `task` | 用户级 TodoList 任务（与 §6.8 配合） |
| `step` | 编排步骤（plan-step） |
| `default` | 兜底 — 客户端不识别其他值时使用 |

**约定**：服务端 MAY 在未来 MINOR 版本新增 icon 值；客户端 MUST 在不识别时 fallback 到 `default`，不应报错。

#### 6.13.3 Metrics（性能/成本）

仅在终态事件（`*.finished` / `*.completed` / `*.failed`）上填写：

```json
"metrics": {
  "duration_ms": 2950,
  "tokens_in": 80,
  "tokens_out": 320,
  "cost_usd": 0.0021,
  "model": "claude-opus-4-7"
}
```

#### 6.13.4 失败事件的统一错误结构

所有 `*.failed` 事件（`trace.failed` / `plan.failed` / `step.failed`）的 `payload.error` 与 §6.12 `error` **同形**，方便监控规则同时匹配两类事件。

```json
"payload": {
  "error": {
    "type": "tool_timeout",
    "code": "WEBFETCH_TIMEOUT",
    "message": "webfetch deadline exceeded after 120s",
    "user_message": "查得有点慢，我换个方法",
    "retryable": true
  }
}
```

| 字段 | 必填 | 说明 |
|------|:---:|------|
| `type` | ✓ | 受控枚举（见下表）。客户端不识别时按 `internal_error` 处理 |
| `code` | – | 自由格式机器码（如 `BASH_TIMEOUT`），由产生错误的工具/组件定义 |
| `message` | ✓ | 给开发者看的描述（可能含 stack/命令/内部 ID） |
| `user_message` | – | persona-friendly 回复。**persona 引用此字段**而非 `message` |
| `retryable` | – | 自动重试是否合理 |

**`error.type` 受控枚举**（与 §6.12 共用一套，emit 失败事件可能用以下任一值）：

| 来源 | type 取值 |
|------|----------|
| §6.12 连接级 | `authentication_error` / `permission_error` / `not_found_error` / `rate_limit_error` / `invalid_request_error` / `overloaded_error` / `internal_error` |
| 工具执行 | `tool_timeout` / `tool_rate_limited` / `tool_invalid_input` |
| LLM | `llm_timeout` / `llm_content_filter` |
| Agent / 编排 | `agent_max_turns_exceeded` / `dependency_failed` / `orphan_timeout` / `aborted` |

> **关键设计**: 把语义放在 `type` 而不是埋在 `message` 字符串里。监控规则可以做 `WHERE error.type = "tool_timeout"`，而不是脆弱的字符串匹配。

#### 6.13.5 Trace 事件

##### `trace.started` — 请求开始

```json
{
  "type": "trace.started",
  "event_id": "evt_xxx",
  "session_id": "sess_abc",
  "request_id": "evt_client_001",
  "envelope": {"trace_id":"tr_xxx","seq":1,"agent_role":"persona","agent_id":"main","severity":"info","timestamp":"..."},
  "display": {"title":"新对话开始","visibility":"collapsed"},
  "payload": {
    "user_input_summary": "用户想查 2024 新能源车销量"
  }
}
```

##### `trace.finished` — 请求成功结束

```json
{
  "type": "trace.finished",
  "event_id": "evt_yyy",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":42,"agent_role":"persona","severity":"info","timestamp":"..."},
  "display": {"title":"对话已完成","visibility":"collapsed"},
  "metrics": {"duration_ms":2950,"tokens_in":350,"tokens_out":120},
  "payload": {
    "output_summary": "",
    "num_turns": 3
  }
}
```

##### `trace.failed` — 请求失败

```json
{
  "type": "trace.failed",
  "event_id": "evt_zzz",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":42,"agent_role":"persona","severity":"error","timestamp":"..."},
  "display": {"title":"对话失败"},
  "metrics": {"duration_ms":1200},
  "payload": {
    "error": {
      "type": "agent_max_turns_exceeded",
      "message": "max_turns reached after 50 turns",
      "user_message": "我有点绕进死循环了，能换个角度问吗？",
      "retryable": false
    }
  }
}
```

#### 6.13.6 Plan 事件（编排）

`plan.created` 在 orchestrator 完成任务拆分后立即 emit，携带完整的任务图：

```json
{
  "type": "plan.created",
  "event_id": "evt_p1",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":5,"task_id":"plan_abc12345","agent_role":"orchestrator","agent_id":"plan_agent","severity":"info","timestamp":"..."},
  "display": {"title":"计划已制定","summary":"比对营收","icon":"plan","visibility":"default"},
  "payload": {
    "plan_id": "plan_abc12345",
    "goal": "查询并对比新能源车销量数据",
    "strategy": "parallel",
    "status": "created",
    "tasks": [
      {"task_id":"s1","subagent_type":"search_agent","user_facing_title":"查 2024 销量"},
      {"task_id":"s2","subagent_type":"search_agent","user_facing_title":"查 2023 销量"},
      {"task_id":"s3","subagent_type":"analysis_agent","depends_on":["s1","s2"],"user_facing_title":"对比同比变化"}
    ]
  }
}
```

`plan.updated` 在 re-plan 时 emit（payload 同 plan.created，status=`updated`）。

`plan.completed` 在所有步骤进入终态且**至少一个**步骤成功时 emit（携带 metrics）。

`plan.failed` 在 plan 整体失败时 emit（如：planner 多次重试仍无法生成有效 plan，或所有步骤都失败）。Payload 用 §6.13.4 统一错误结构：

```json
{
  "type": "plan.failed",
  "event_id": "evt_pf",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":30,"task_id":"plan_abc12345","agent_role":"orchestrator","severity":"error","timestamp":"..."},
  "display": {"title":"计划失败","summary":"0/3 步骤成功","icon":"error"},
  "metrics": {"duration_ms":4200},
  "payload": {
    "error": {
      "type": "dependency_failed",
      "message": "all 3 steps failed: ..."
    }
  }
}
```

#### 6.13.7 Step 事件（编排步骤）

> **命名空间隔离**: 这些事件在 `step.*` 前缀下，**不与** §6.8 的用户级 TodoList `task.*` 冲突。Payload 使用 `step_id` 字段（不叫 `task_id`，避免再次混淆）。

##### Step 生命周期闭环

| 事件 | 触发时机 | 必有后续 |
|------|---------|---------|
| `step.dispatched` | orchestrator 派发任务（可能在 wave 队列中等待） | `step.started` 或 `step.skipped` |
| `step.started` | worker 实际开始执行 | `step.completed` 或 `step.failed` |
| `step.progress` | 长任务进度心跳（节流） | — |
| `step.completed` | worker 成功返回 | — |
| `step.failed` | worker 失败 / 超时 / 中止 | — |
| `step.skipped` | 上游依赖失败导致取消 | — |

> **孤儿兜底**：若 worker 进程崩溃或在窗口期内未产出 `step.completed/failed`，框架在超时后自动 emit `step.failed { error.type: "orphan_timeout" }`。**永不留下 dispatched 没有终态的步骤**。

##### `step.dispatched` — 派发任务

`step.dispatched` 是父侧（orchestrator）记录"我派出去了"。worker 侧的"实际启动"用 `step.started` 表示。两者之间的间隔是 wave 队列等待时间。

```json
{
  "type": "step.dispatched",
  "event_id": "evt_sd",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":6,"task_id":"s1","parent_task_id":"plan_abc12345","agent_role":"orchestrator","severity":"info","timestamp":"..."},
  "display": {"title":"派出 search_agent","summary":"查 2024 销量","icon":"dispatch"},
  "payload": {
    "step_id": "s1",
    "subagent_type": "search_agent",
    "input_summary": "查 2024 中国新能源车销量"
  }
}
```

##### `step.started` — 子 Agent 实际开始执行

```json
{
  "type": "step.started",
  "event_id": "evt_ss",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":7,"task_id":"s1","parent_task_id":"plan_abc12345","agent_role":"orchestrator","severity":"info","timestamp":"..."},
  "display": {"title":"s1 开始","visibility":"collapsed"},
  "payload": {
    "step_id": "s1",
    "agent_id": "agent_xxx"
  }
}
```

> **与既有 `subagent.start` 的关系**: `subagent.start`（§6.6）是 worker 自身的启动事件（保留向前兼容、不带 envelope）；`step.started` 是 orchestrator 视角的对应事件（带 envelope，用于编排树渲染）。两者在大多数情况下背靠背出现，客户端可二选一渲染。

##### `step.completed` — 步骤成功

```json
{
  "type": "step.completed",
  "event_id": "evt_sc",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":12,"task_id":"s1","severity":"info","timestamp":"..."},
  "display": {"title":"✓ s1 完成","summary":"找到 5 篇报告","icon":"success"},
  "metrics": {"duration_ms":1800,"tokens_in":80,"tokens_out":320},
  "payload": {
    "step_id": "s1",
    "output_summary": "2024 销量约 1100 万辆",
    "attempts": 1,
    "deliverables": ["/tmp/sales-2024.md"]
  }
}
```

##### `step.failed` — 步骤失败

```json
{
  "type": "step.failed",
  "event_id": "evt_sf",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":13,"task_id":"s2","severity":"error","timestamp":"..."},
  "display": {"title":"✗ s2 失败","summary":"webfetch 超时","icon":"error"},
  "metrics": {"duration_ms":120000},
  "payload": {
    "error": {
      "type": "tool_timeout",
      "code": "WEBFETCH_TIMEOUT",
      "message": "webfetch deadline exceeded after 120s",
      "user_message": "查得有点慢，我换个方法",
      "retryable": true
    }
  }
}
```

##### `step.skipped` — 因依赖失败被跳过

```json
{
  "type": "step.skipped",
  "event_id": "evt_sk",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":14,"task_id":"s3","severity":"warn","timestamp":"..."},
  "display": {"title":"跳过 s3","summary":"上游 s2 失败","icon":"warning"},
  "payload": {
    "step_id": "s3",
    "reason": "upstream step s2 failed"
  }
}
```

##### `step.progress` — 进度心跳（必须节流）

```json
{
  "type": "step.progress",
  "event_id": "evt_sp",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":7,"task_id":"s1","severity":"info","timestamp":"..."},
  "payload": {
    "step_id": "s1",
    "progress_pct": 0.4,
    "stage": "fetching_results",
    "items_processed": 12
  }
}
```

> **节流要求**: 服务端 SHOULD 在 200–500ms 内不重复 emit 同一 step 的 progress；客户端应做 throttle 渲染。`step.progress` 的优先级低于 `step.completed/failed`——背压情况下可丢弃。

#### 6.13.8 Worker 生命周期与既有 `subagent.*` 的整合

worker 自身的启动 / 结束事件由既有 §6.6 `subagent.start` / `subagent.end` 表示（**不**重新发明 `worker.started/finished`）。这两个事件**保留原有顶层字段格式**，不引入 envelope，以保证向前兼容。

| 视角 | 事件 | 说明 |
|------|------|------|
| orchestrator 视角 | `step.dispatched` → `step.started` → `step.completed/failed/skipped` | 带 envelope，用于编排树渲染 |
| worker 自身视角 | `subagent.start` → `subagent.event*` → `subagent.end` | 不带 envelope，沿用 §6.6 协议 |
| worker 内部 | `agent.heartbeat`（带 envelope） | 长任务存活信号 |

客户端可按需求选择消费哪一组：
- 只关心编排树（多数情况） → 监听 `step.*`，用 envelope.task_id 聚合
- 关心 worker 内部细节（如调试） → 加挂 `subagent.*` 流

#### 6.13.9 Agent 心跳

##### `agent.heartbeat` — 长任务存活信号

```json
{
  "type": "agent.heartbeat",
  "event_id": "evt_hb",
  "session_id": "sess_abc",
  "envelope": {"trace_id":"tr_xxx","seq":20,"agent_id":"agent_xxx","agent_role":"worker","severity":"info","timestamp":"..."},
  "payload": {
    "agent_id": "agent_xxx",
    "stage": "running_tools",
    "uptime_ms": 8500
  }
}
```

> 长任务（> 5 秒）应每 5–10 秒 emit 一次心跳。监控应关注「started 但永远没结束」的孤儿事件——这是多 Agent 系统最常见的 bug。

#### 6.13.10 触发机制：框架触发，不是 Agent 触发

Emit **必须由框架层（确定性代码）触发**，而不是由 LLM 自行决定：

| 触发者 | 负责什么 |
|--------|---------|
| **框架层（确定性代码）** | 决定**何时** emit、emit **什么类型**、填充 envelope |
| **Agent（LLM）** | 通过结构化输出**提供素材**：title / summary / user_facing_description |

服务端实现保证：
- **生命周期闭环**：每个 `*.started` / `*.dispatched` / `*.created` 必有对应的 `*.finished` / `*.completed` / `*.failed` / `*.skipped`，异常路径也会兜底（panic recovery、orphan-timeout 都会 emit `*.failed`）
- **幂等去重**：每个事件携带唯一 `event_id`，客户端按 event_id 去重；emit 设计为「至少一次投递」
- **背压保护**：`*.started` / `*.completed` / `*.failed` 必须送达；`*.progress` / `*.heartbeat` 可丢弃
- **大数据走引用**：单个 emit 事件 < 4KB；大输出（如全文报告）通过 `output_ref` 字段引用 Blob 存储，不直接进事件流

### 6.14 Session Resume Responses (v1.11+)

服务端对客户端 `session.resume`（§5.7）的响应。流程详见 §3.6。

#### `session.resumed` — 续传成功

```json
{
  "type": "session.resumed",
  "event_id": "evt_resumed",
  "session_id": "sess_abc",
  "trace_id": "tr_xxx",
  "from_seq": 13,
  "to_seq": 42
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `trace_id` | string | 续传的 trace |
| `from_seq` | int64 | 第一条补发事件的 `envelope.seq`（含） |
| `to_seq` | int64 | 最后一条补发事件的 `envelope.seq`（含） |

服务端在此消息**之后**按原序补发所有 `from_seq <= seq <= to_seq` 的事件，然后过渡到正常实时投递。

#### `session.resume_failed` — 续传不可行

```json
{
  "type": "session.resume_failed",
  "event_id": "evt_rf",
  "session_id": "sess_abc",
  "trace_id": "tr_xxx",
  "reason": "events_expired"
}
```

| reason | 说明 | 客户端动作 |
|--------|------|----------|
| `events_expired` | 超出留存窗口（默认 5 分钟） | 全量刷新（REST 历史 API） |
| `unknown_trace` | trace_id 不存在 | 视为 trace 已结束，丢弃本地状态 |
| `session_not_found` | session 已被回收 | 重建 session |
| `not_implemented` | 服务端尚未启用留存（v1.11 当前状态） | 全量刷新 |

---

## 7. Event Sequences

### 7.1 纯文本对话

```
Client                                         Server
  |                                              |
  |  ─── session.create ──────────────────────→  |
  |      {session_id:"sess_abc123"}              |
  |  ←─── session.created ──────────────────────  |
  |       {protocol_version:"1.7",session:{...}}  |
  |                                              |
  |  ─── user.message ───────────────────────→   |
  |      {event_id:"c1",content:{text:"Hi"}}     |
  |                                              |
  |  ←─── message.start ────────────────────────  |
  |       {request_id:"c1",message:{id:"msg_1"}} |
  |  ←─── content.start ────────────────────────  |
  |       {index:0,content_block:{type:"text"}}   |
  |  ←─── content.delta ────────────────────────  |
  |       {index:0,delta:{type:"text_delta",      |
  |        text:"Hello"}}                         |
  |  ←─── content.delta ────────────────────────  |
  |       {index:0,delta:{type:"text_delta",      |
  |        text:" World!"}}                       |
  |  ←─── content.stop ─────────────────────────  |
  |       {index:0}                               |
  |  ←─── message.delta ────────────────────────  |
  |       {delta:{stop_reason:"end_turn"},        |
  |        usage:{output_tokens:5}}               |
  |  ←─── message.stop ─────────────────────────  |
  |                                              |
  |  ←─── task.end ──────────────────────────────  |
  |       {request_id:"c1",status:"success",      |
  |        num_turns:1,duration_ms:856}           |
```

### 7.2 带客户端工具执行（multi-turn）

> 适用于 `session.capabilities.client_tools: true` 模式。

```
→ user.message           {event_id:"c1", content:{text:"List files"}}

  ── Turn 1: LLM 输出文本 + tool_use ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"Let me check:"}}
← content.stop           {index:0}
← content.start          {index:1, content_block:{type:"tool_use", id:"toolu_1", name:"bash"}}
← content.delta          {index:1, delta:{type:"input_json_delta", partial_json:"{\"command\":"}}
← content.delta          {index:1, delta:{type:"input_json_delta", partial_json:"\"ls -la\"}"}}
← content.stop           {index:1}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:30}}
← message.stop

  ── 服务端下发工具调用 ──
← tool.call              {tool_use_id:"toolu_1", tool_name:"bash", input:{command:"ls -la"}}

  ── 客户端本地执行 bash 并回传结果 ──
→ tool.result            {tool_use_id:"toolu_1", status:"success",
                           output:"total 48\ndrwxr-xr-x...",
                           metadata:{exit_code:0, duration_ms:50}}

  ── Turn 2: LLM 基于工具结果生成最终回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"Here are the files:..."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:50}}
← message.stop

  ── Query-loop 结束 ──
← task.end               {request_id:"c1", status:"success", num_turns:2, duration_ms:3200}
```

### 7.3 带服务端工具执行（multi-turn）

> 适用于 `session.capabilities.client_tools: false` 模式。服务端直接执行工具，客户端通过 `tool.start`/`tool.end` 事件观察执行过程。

```
→ user.message           {event_id:"c1", content:{text:"List files"}}

  ── Turn 1: LLM 输出文本 + tool_use ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"Let me check:"}}
← content.stop           {index:0}
← content.start          {index:1, content_block:{type:"tool_use", id:"toolu_1", name:"bash"}}
← content.delta          {index:1, delta:{type:"input_json_delta", partial_json:"{\"command\":"}}
← content.delta          {index:1, delta:{type:"input_json_delta", partial_json:"\"ls -la\"}"}}
← content.stop           {index:1}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:30}}
← message.stop

  ── 服务端开始执行工具 ──
← tool.start             {tool_use_id:"toolu_1", tool_name:"bash",
                           input:{command:"ls -la"}}

  ── 服务端执行完成 ──
← tool.end               {tool_use_id:"toolu_1", tool_name:"bash",
                           status:"success",
                           output:"total 48\ndrwxr-xr-x...",
                           is_error:false, duration_ms:50,
                           metadata:{exit_code:0, duration_ms:50}}

  ── Turn 2: LLM 基于工具结果生成最终回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"Here are the files:..."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:50}}
← message.stop

  ── Query-loop 结束 ──
← task.end               {request_id:"c1", status:"success", num_turns:2, duration_ms:3200}
```

**与客户端工具执行的关键区别**:
- 客户端模式：`tool.call` → 客户端执行 → `tool.result`（客户端回传）
- 服务端模式：`tool.start` → 服务端执行 → `tool.end`（服务端推送）
- 两种模式下，LLM 的 `tool_use` 输出都通过 `content.*` 事件传递，格式完全一致

### 7.4 工具被安全策略拒绝

```
→ user.message           {event_id:"c1", content:{text:"Delete all files"}}

← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"tool_use", id:"toolu_1", name:"bash"}}
← content.delta          {index:0, delta:{type:"input_json_delta",
                           partial_json:"{\"command\":\"rm -rf /\"}"}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:15}}
← message.stop

← tool.call              {tool_use_id:"toolu_1", tool_name:"bash",
                           input:{command:"rm -rf /"}}

  ── 客户端安全策略拒绝执行 ──
→ tool.result            {tool_use_id:"toolu_1", status:"denied",
                           error:{code:"permission_denied",
                                  message:"Dangerous command blocked by security policy"}}

  ── LLM 收到拒绝信息，生成替代回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"I'm unable to execute that command..."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:20}}
← message.stop

← task.end               {request_id:"c1", status:"success", num_turns:2}
```

### 7.5 工具执行超时

```
← tool.call              {tool_use_id:"toolu_1", tool_name:"bash",
                           input:{command:"make build"}}

  ── 客户端发送心跳延长超时 ──
→ tool.progress          {tool_use_id:"toolu_1", output:"Compiling... 30%"}
→ tool.progress          {tool_use_id:"toolu_1", output:"Compiling... 80%"}

  ── 超时，客户端 kill 进程 ──
→ tool.result            {tool_use_id:"toolu_1", status:"timeout",
                           error:{code:"execution_timeout",
                                  message:"Command timed out after 120s"},
                           metadata:{duration_ms:120000}}
```

### 7.6 错误场景

```
→ user.message           {event_id:"c2", content:{text:"..."}}
← error                  {request_id:"c2",
                           error:{type:"rate_limit_error",
                                  code:"rate_limit_exceeded",
                                  message:"Too many requests",
                                  retry_after_ms:30000}}
← task.end               {request_id:"c2", status:"error", num_turns:0}
```

### 7.7 用户中断

```
→ user.message           {event_id:"c3", content:{text:"Write a long essay"}}
← message.start          {request_id:"c3", message:{id:"msg_3"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"Sure, "}}

→ session.interrupt      {event_id:"c4"}

← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:3}}
← message.stop
← task.end               {request_id:"c3", status:"aborted", num_turns:1}
```

### 7.8 权限审批流程（服务端工具执行）

> 适用于 `session.capabilities.client_tools: false` 模式，且工具需要用户确认（如 default 模式下的写操作）。

```
→ user.message           {event_id:"c1", content:{text:"Push the current branch"}}

  ── Turn 1: LLM 输出 tool_use ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"tool_use", id:"toolu_1", name:"Bash"}}
← content.delta          {index:0, delta:{type:"input_json_delta",
                           partial_json:"{\"command\":\"git push origin main\"}"}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:15}}
← message.stop

  ── 服务端权限检查: Ask → 发送审批请求（含三选项） ──
← permission.request     {request_id:"perm_a1b2c3d4", tool_name:"Bash",
                           tool_input:"{\"command\":\"git push origin main\"}",
                           message:"Allow git push to make changes?",
                           is_read_only:false,
                           permission_key:"Bash:git push",
                           options:[
                             {label:"Allow once", scope:"once", allow:true},
                             {label:"Always allow git push in this session",
                              scope:"session", allow:true},
                             {label:"Deny", scope:"once", allow:false}
                           ]}

  ── 客户端展示审批 UI，用户选择「会话级允许 git push」 ──
→ permission.response    {request_id:"perm_a1b2c3d4", approved:true,
                           scope:"session"}

  ── 审批通过，服务端开始执行工具 ──
  ── 后续同一会话内 git push 将自动审批 ──
  ── 注意：git status、git add 等仍需单独确认 ──
← tool.start             {tool_use_id:"toolu_1", tool_name:"Bash",
                           input:{command:"git push origin main"}}
← tool.end               {tool_use_id:"toolu_1", tool_name:"Bash",
                           status:"success", output:"", is_error:false}

  ── Turn 2: LLM 基于工具结果回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"Done. Pushed to origin/main."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:15}}
← message.stop

← task.end               {request_id:"c1", status:"success", num_turns:2}
```

### 7.8.1 权限被拒绝

```
  ── 服务端权限检查: Ask → 发送审批请求 ──
← permission.request     {request_id:"perm_a1b2c3d4", tool_name:"Bash",
                           tool_input:"{\"command\":\"rm -rf /\"}",
                           message:"Allow Bash to make changes?",
                           is_read_only:false,
                           options:[...]}

  ── 用户选择「拒绝」 ──
→ permission.response    {request_id:"perm_a1b2c3d4", approved:false,
                           message:"Dangerous command blocked by user"}

  ── 服务端将拒绝信息传给 LLM ──
  ── Turn 2: LLM 收到权限拒绝，生成替代回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"I understand. I won't execute that command."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:12}}
← message.stop

← task.end               {request_id:"c1", status:"success", num_turns:2}
```

### 7.8.2 会话级自动审批

> 用户此前已选择「会话级允许 git push」，后续 `git push` 调用直接执行。
> 但 `git status`、`git add` 等其它 git 子命令仍需独立审批。

```
→ user.message           {event_id:"c2", content:{text:"Push the feature branch too"}}

  ── Turn 1: LLM 输出 tool_use ──
← message.start          {request_id:"c2", message:{id:"msg_3"}}
← content.start          {index:0, content_block:{type:"tool_use", id:"toolu_2", name:"Bash"}}
← content.delta          {index:0, delta:{type:"input_json_delta",
                           partial_json:"{\"command\":\"git push origin feature\"}"}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:10}}
← message.stop

  ── 服务端权限检查: Bash:git push 已被会话级允许 → 自动审批（无 permission.request） ──
← tool.start             {tool_use_id:"toolu_2", tool_name:"Bash",
                           input:{command:"git push origin feature"}}
← tool.end               {tool_use_id:"toolu_2", tool_name:"Bash",
                           status:"success", output:"", is_error:false}

  ── Turn 2: LLM 回复 ──
← message.start          {request_id:"c2", message:{id:"msg_4"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"Done. Feature branch pushed."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:20}}
← message.stop

← task.end               {request_id:"c2", status:"success", num_turns:2}
```

### 7.9 子 Agent 执行（服务端工具模式）

> 适用于 `session.capabilities.sub_agents: true` 时。当 LLM 调用 Agent 工具生成子 Agent 时，客户端通过 `subagent.start`/`subagent.event`/`subagent.end` 实时感知子 Agent 生命周期和执行过程。

```
→ user.message           {event_id:"c1", content:{text:"Search the codebase for auth bugs"}}

  ── Turn 1: LLM 输出文本 + tool_use (Agent) ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"Let me search the codebase."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:20}}
← message.stop

  ── 服务端执行 Agent 工具 → 生成子 Agent ──
← tool.start             {tool_use_id:"toolu_1", tool_name:"Agent",
                           input:{prompt:"Search for auth vulnerabilities",
                                  subagent_type:"Explore",
                                  description:"search auth bugs"}}

  ── 子 Agent 开始（客户端显示 spinner） ──
← subagent.start         {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           description:"search auth bugs",
                           agent_type:"sync",
                           parent_agent_id:"main"}

  ── 子 Agent 实时流式推送（客户端渐进渲染） ──
← subagent.event         {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           payload:{event_type:"text",
                                    text:"Let me search for auth..."}}
← subagent.event         {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           payload:{event_type:"tool_start",
                                    tool_name:"Grep",
                                    tool_use_id:"toolu_sub_1",
                                    tool_input:"{...}"}}
← subagent.event         {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           payload:{event_type:"tool_end",
                                    tool_name:"Grep",
                                    tool_use_id:"toolu_sub_1",
                                    output:"Found 3 matches...",
                                    is_error:false}}
← subagent.event         {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           payload:{event_type:"text",
                                    text:"Found 3 potential auth issues..."}}

  ── 子 Agent 完成（客户端关闭 spinner） ──
← subagent.end           {agent_id:"sess_abc123_sub_e5f6g7h8",
                           agent_name:"search auth bugs",
                           status:"completed",
                           duration_ms:8500,
                           num_turns:3,
                           usage:{input_tokens:12000, output_tokens:2500},
                           denied_tools:[]}

  ── Agent 工具执行完成 ──
← tool.end               {tool_use_id:"toolu_1", tool_name:"Agent",
                           status:"success",
                           output:"Found 3 potential auth issues...",
                           is_error:false, duration_ms:8600,
                           metadata:{agent_id:"sess_abc123_sub_e5f6g7h8",
                                     num_turns:3}}

  ── Turn 2: LLM 基于子 Agent 结果生成最终回复 ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"I found 3 potential authentication issues..."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:80}}
← message.stop

← task.end               {request_id:"c1", status:"success", num_turns:2}
```

**关键时序**:
- `tool.start`（Agent 工具）→ `subagent.start` → `subagent.event`* → `subagent.end` → `tool.end`（Agent 工具）
- `subagent.event` 在 `subagent.start` 和 `subagent.end` 之间可出现零次或多次
- 子 Agent 的 `subagent.*` 事件嵌套在父 Agent 的 `tool.start`/`tool.end` 之间
- 客户端应使用 `subagent.start` 触发 spinner/加载指示器，`subagent.event` 渐进渲染内容，`subagent.end` 关闭

### 7.10 @-Mention 路由

> 用户消息中包含 @-mention 时，服务端将请求路由到对应 Agent。

```
→ user.message           {event_id:"c1", content:{text:"@backend-dev implement the auth API"}}

  ── 服务端识别 @-mention，路由到 backend-dev ──
← agent.routed           {agent_id:"agent_bd1", agent_name:"backend-dev",
                           description:"Handling backend implementation",
                           agent_type:"general-purpose"}

  ── Turn 1: 被路由的 Agent 开始执行 ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"I'll implement the auth API..."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:50}}
← message.stop

← task.end               {request_id:"c1", status:"success", num_turns:1}
```

### 7.11 异步 Agent 执行

> `capabilities.async_agent: true` 时。Agent 工具设置 `run_in_background: true`，子 Agent 后台运行。

```
→ user.message           {event_id:"c1", content:{text:"Analyze the codebase in background"}}

  ── Turn 1: LLM 请求生成异步 Agent ──
← message.start          {request_id:"c1", message:{id:"msg_1"}}
← content.start          {index:0, content_block:{type:"tool_use", id:"toolu_1", name:"Agent"}}
← content.delta          {index:0, delta:{type:"input_json_delta",
                           partial_json:"{\"prompt\":\"Analyze codebase\",\"run_in_background\":true}"}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"tool_use"}, usage:{output_tokens:25}}
← message.stop

  ── 服务端执行 Agent 工具 → 生成异步 Agent（立即返回） ──
← tool.start             {tool_use_id:"toolu_1", tool_name:"Agent",
                           input:{prompt:"Analyze codebase", run_in_background:true}}
← agent.spawned          {agent_id:"agent_c9d0e1f2",
                           agent_name:"analyzer",
                           description:"Analyze codebase",
                           agent_type:"async",
                           parent_agent_id:"main"}
← tool.end               {tool_use_id:"toolu_1", tool_name:"Agent",
                           status:"success",
                           output:"Agent spawned: agent_c9d0e1f2"}

  ── Turn 2: LLM 继续处理（不等待异步 Agent） ──
← message.start          {request_id:"c1", message:{id:"msg_2"}}
← content.delta          {index:0, delta:{type:"text_delta",
                           text:"I've started the analysis in the background."}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}}
← message.stop
← task.end               {request_id:"c1", status:"success", num_turns:2}

  ── 后续：异步 Agent 在后台继续执行 ──
← agent.idle             {agent_id:"agent_c9d0e1f2", agent_name:"analyzer"}
  ...
← agent.completed        {agent_id:"agent_c9d0e1f2",
                           status:"completed", duration_ms:45000,
                           usage:{input_tokens:50000, output_tokens:12000}}
```

### 7.12 团队协作

> `capabilities.teams: true` 时。Coordinator 创建团队，生成 teammates，通过任务系统协调工作。

```
→ user.message           {event_id:"c1", content:{text:"Build a full-stack feature with a team"}}

  ── Coordinator 创建团队 ──
← team.created           {team:{team_id:"team_abc",
                           team_name:"fullstack-feature",
                           members:["coordinator"]}}

  ── Coordinator 生成 teammates ──
← agent.spawned          {agent_id:"agent_fe1", agent_name:"frontend-dev",
                           agent_type:"async", parent_agent_id:"coordinator"}
← team.member_join       {team:{team_id:"team_abc",
                           member_name:"frontend-dev", member_type:"teammate",
                           members:["coordinator","frontend-dev"]}}

← agent.spawned          {agent_id:"agent_be1", agent_name:"backend-dev",
                           agent_type:"async", parent_agent_id:"coordinator"}
← team.member_join       {team:{team_id:"team_abc",
                           member_name:"backend-dev", member_type:"teammate",
                           members:["coordinator","frontend-dev","backend-dev"]}}

  ── Coordinator 创建任务 ──
← task.created           {task:{task_id:"1", subject:"Build API endpoints",
                           status:"pending", scope_id:"team_abc"}}
← task.created           {task:{task_id:"2", subject:"Build React components",
                           status:"pending", scope_id:"team_abc"}}

  ── 任务分配和执行 ──
← task.updated           {task:{task_id:"1", status:"in_progress",
                           owner:"backend-dev",
                           active_form:"Building API endpoints",
                           scope_id:"team_abc"}}
← task.updated           {task:{task_id:"2", status:"in_progress",
                           owner:"frontend-dev",
                           active_form:"Building React components",
                           scope_id:"team_abc"}}

  ── Agent 间通信 ──
← agent.message          {message:{from:"backend-dev", to:"coordinator",
                           summary:"API endpoints complete",
                           team_id:"team_abc"}}
← task.updated           {task:{task_id:"1", status:"completed",
                           scope_id:"team_abc"}}

  ── 团队解散 ──
← team.member_left       {team:{team_id:"team_abc",
                           member_name:"frontend-dev",
                           members:["coordinator","backend-dev"]}}
← team.member_left       {team:{team_id:"team_abc",
                           member_name:"backend-dev",
                           members:["coordinator"]}}
← team.deleted           {team:{team_id:"team_abc",
                           team_name:"fullstack-feature"}}

← task.end               {request_id:"c1", status:"success"}
```

### 7.13 Emit 生命周期（v1.11+）

> 一个完整的 Orchestrate 请求示意：trace 包住整个请求，plan 包住编排，每个 step 嵌在 plan 之内，工具调用嵌在 step 之内。

```
→ user.message              {event_id:"c1", text:"比对营收 A 和 B"}

  ── 框架 emit trace.started（persona） ──
← trace.started             {event_id:"evt_t1", session_id:"sess_abc",
                              envelope:{trace_id:"tr_xxx",seq:1,agent_role:"persona",
                                agent_id:"main",severity:"info",timestamp:"..."},
                              display:{title:"新对话开始"},
                              payload:{user_input_summary:"比对营收 A 和 B"}}

  ── persona（emma）输出 + tool_use(Orchestrate) ──
← message.start             {request_id:"c1",...}
← content.start / delta / stop
← message.delta             {delta:{stop_reason:"tool_use"}}
← message.stop

  ── 服务端执行 Orchestrate 工具 ──
← tool.start                {tool_use_id:"toolu_1", tool_name:"Orchestrate", ...}

  ── orchestrator 完成拆分，框架 emit plan.created ──
← plan.created              {event_id:"evt_p1", session_id:"sess_abc",
                              envelope:{trace_id:"tr_xxx",seq:5,agent_role:"orchestrator",
                                agent_id:"plan_agent", task_id:"plan_abc12345",...},
                              display:{title:"计划已制定",icon:"plan"},
                              payload:{plan_id:"plan_abc12345", goal:"比对营收 A 和 B",
                                strategy:"parallel", status:"created",
                                tasks:[
                                  {task_id:"s1",subagent_type:"researcher",user_facing_title:"查 A 营收"},
                                  {task_id:"s2",subagent_type:"researcher",user_facing_title:"查 B 营收"},
                                  {task_id:"s3",subagent_type:"analyst",depends_on:["s1","s2"]}
                                ]}}

  ── 第一波（s1 + s2 并行）派发 + 启动 ──
← step.dispatched           {envelope:{seq:6,task_id:"s1",parent_task_id:"plan_abc12345",
                                agent_role:"orchestrator",...},
                              display:{title:"派出 researcher",summary:"查 A 营收",icon:"dispatch"},
                              payload:{step_id:"s1",subagent_type:"researcher",...}}
← step.dispatched           {envelope:{seq:7,task_id:"s2",...},
                              payload:{step_id:"s2",subagent_type:"researcher",...}}
← step.started              {envelope:{seq:8,task_id:"s1",...},
                              payload:{step_id:"s1",agent_id:"agent_xxx"}}
← step.started              {envelope:{seq:9,task_id:"s2",...},
                              payload:{step_id:"s2",agent_id:"agent_yyy"}}

  ── worker 自身的 subagent.* 事件（既有 §6.6 协议，与 emit 并存） ──
← subagent.start            {agent_id:"agent_xxx", agent_name:"s1",...}
← subagent.event            {payload:{event_type:"tool_start",tool_name:"Grep",...}}
← subagent.event            {payload:{event_type:"tool_end",tool_name:"Grep",...}}
← subagent.end              {agent_id:"agent_xxx", status:"completed",...}

  ── 框架 emit step.completed（带 metrics） ──
← step.completed            {envelope:{seq:18,task_id:"s1",severity:"info",...},
                              display:{title:"✓ s1 完成",icon:"success"},
                              metrics:{duration_ms:1800,tokens_in:80,tokens_out:320},
                              payload:{step_id:"s1",output_summary:"A 营收 $2B",attempts:1}}
← step.completed            {envelope:{seq:19,task_id:"s2",...},...}

  ── 第二波（s3 依赖已就绪） ──
← step.dispatched           {envelope:{seq:20,task_id:"s3",...},...}
← step.started              {envelope:{seq:21,task_id:"s3",...},...}
← step.completed            {envelope:{seq:25,task_id:"s3",...},...}

  ── 计划完成 ──
← plan.completed            {envelope:{seq:26,task_id:"plan_abc12345",
                                agent_role:"orchestrator",...},
                              display:{title:"计划完成",summary:"3/3 步骤成功",icon:"plan"},
                              metrics:{duration_ms:4200},
                              payload:{plan_id:"plan_abc12345",status:"completed",tasks:[...]}}

  ── Orchestrate 工具结束，回到 persona ──
← tool.end                  {tool_use_id:"toolu_1", status:"success",...}

  ── persona 基于结果回复 ──
← message.start             {...}
← content.delta             {delta:{type:"text_delta",text:"比对结果如下..."}}
...

  ── 框架 emit trace.finished ──
← trace.finished            {envelope:{seq:42,agent_role:"persona",...},
                              display:{title:"对话已完成",visibility:"collapsed"},
                              metrics:{duration_ms:5200,tokens_in:850,tokens_out:420},
                              payload:{num_turns:3}}

← task.end                  {request_id:"c1",status:"success",num_turns:3,...}
```

**关键时序**:
- `trace.started` 是请求的第一条 emit 事件，`trace.finished`/`trace.failed` 是最后一条
- `plan.created` → 多个 `step.dispatched` → 对应数量的 `step.started` → 对应数量的 `step.completed`/`step.failed`/`step.skipped` → `plan.completed` 或 `plan.failed`
- 每个 `step.*` 事件的 `envelope.parent_task_id` 都指向同一个 plan_id
- 客户端可按 `envelope.task_id` 聚合卡片：plan card 含子 step cards，step card 含工具 cards
- 失败路径：任何一层失败都保证 emit 对应的 `*.failed`，**永不留下孤儿 started**（孤儿超时由框架兜底 emit `step.failed { error.type: "orphan_timeout" }`）
- 顶层 `event_id` 和 `session_id` 在每条 emit 事件上**仍然存在**（与 §4 一致），envelope 不重复

### 7.14 Extended Thinking（预留）

```
→ user.message           {event_id:"c5", content:{text:"Solve this complex problem"}}
← message.start          {request_id:"c5", message:{id:"msg_5"}}
← content.start          {index:0, content_block:{type:"thinking"}}
← content.delta          {index:0, delta:{type:"thinking_delta", thinking:"Let me analyze..."}}
← content.delta          {index:0, delta:{type:"thinking_delta", thinking:"First I need to..."}}
← content.stop           {index:0}
← content.start          {index:1, content_block:{type:"text"}}
← content.delta          {index:1, delta:{type:"text_delta", text:"The answer is..."}}
← content.stop           {index:1}
← message.delta          {delta:{stop_reason:"end_turn"}, usage:{output_tokens:120}}
← message.stop
← task.end               {request_id:"c5", status:"success", num_turns:1}
```

### 7.15 契约式 sub-agent 产出（v1.13+）

完整链路：用户问 → emma 派 Specialists → L2 用 `expected_outputs` 派 writer → L3 写 ArtifactWrite + SubmitTaskResult → 最后 emma 用文件名告诉用户。

```
→ user.message           {event_id:"u1", content:{text:"写一封专业邮件，关于实习生作息安排"}}

← message.start          {message:{id:"msg_1"}}            (emma 开始思考)
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta", text:"好，我让 Specialists 帮你..."}}
← content.stop           {index:0}

# emma 调 Specialists 工具
← content.start          {index:1, content_block:{type:"tool_use", name:"Specialists"}}
← content.delta          {index:1, delta:{type:"input_json_delta", ...}}
← content.stop           {index:1}
← message.delta          {delta:{stop_reason:"tool_use"}}
← message.stop

← agent.intent           {tool_use_id:"tu_main_1", tool_name:"Specialists", intent:"派 Specialists 写邮件"}
← tool.start             {tool_use_id:"tu_main_1", tool_name:"Specialists"}

# L2 Specialists 起
← subagent.start         {agent_id:"sess_x_sub_y", agent_name:"specialists",
                          description:"写实习生作息邮件",
                          task:"写一封专业邮件，关于实习生作息安排"}

# L2 内部读 expected-outputs 决策（不可见）→ 调 Task 派 writer
← subagent.event         {agent_id:"sess_x_sub_y", payload:{
                            event_type:"intent", tool_name:"Task",
                            tool_use_id:"tu_l2_1",
                            intent:"派 writer 写邮件，要求 draft_email 角色"}}
← subagent.event         {agent_id:"sess_x_sub_y", payload:{
                            event_type:"tool_start", tool_name:"Task",
                            tool_use_id:"tu_l2_1",
                            tool_input:"{\"subagent_type\":\"writer\",\"prompt\":\"...\",
                              \"expected_outputs\":[{\"role\":\"draft_email\",\"type\":\"file\",
                              \"min_size_bytes\":100,\"required\":true}]}"}}

# L3 writer 起（事件透传过 L2）
← subagent.start         {agent_id:"sess_x_sub_z", agent_name:"writer",
                          parent_agent_id:"sess_x_sub_y", task:"写邮件..."}

# L3 调 ArtifactWrite —— 实时点亮 artifact 卡（v1.13+）
← subagent.event         {agent_id:"sess_x_sub_z", payload:{
                            event_type:"tool_end", tool_name:"ArtifactWrite",
                            tool_use_id:"tu_l3_1", is_error:false,
                            artifacts:[{artifact_id:"art_a1b2...", name:"intern-schedule-email.md",
                                        type:"file", size_bytes:1240,
                                        description:"实习生作息安排邮件正稿"}]}}

# L3 调 SubmitTaskResult —— 框架 M4 校验通过
← subagent.event         {agent_id:"sess_x_sub_z", payload:{
                            event_type:"tool_end", tool_name:"SubmitTaskResult",
                            tool_use_id:"tu_l3_2", is_error:false,
                            artifacts:[{artifact_id:"art_a1b2...", role:"draft_email",
                                        name:"intern-schedule-email.md", ...}]}}

# L3 收尾 —— 聚合产出（v1.13+）
← subagent.end           {agent_id:"sess_x_sub_z", agent_name:"writer",
                          status:"completed", num_turns:3, duration_ms:8200,
                          artifacts:[{artifact_id:"art_a1b2...", role:"draft_email",
                                      name:"intern-schedule-email.md", ...}]}

← subagent.event         {agent_id:"sess_x_sub_y", payload:{
                            event_type:"tool_end", tool_name:"Task", is_error:false,
                            output:"完成。\n产出 artifact:\n- [draft_email] art_a1b2... — intern-schedule-email.md..."}}

# L2 收尾 —— 透传 L3 提交的 artifact
← subagent.end           {agent_id:"sess_x_sub_y", agent_name:"specialists",
                          status:"completed", num_turns:6, duration_ms:12500,
                          artifacts:[{artifact_id:"art_a1b2...", role:"draft_email",
                                      name:"intern-schedule-email.md", ...}]}

# Specialists 工具收尾 —— L1 trace 锚点（v1.13+），artifacts 字段完整聚合
← tool.end               {tool_use_id:"tu_main_1", tool_name:"Specialists",
                          status:"success", is_error:false, render_hint:"agent",
                          duration_ms:12500,
                          artifacts:[{artifact_id:"art_a1b2...", name:"intern-schedule-email.md",
                                      type:"file", size_bytes:1240,
                                      description:"实习生作息安排邮件正稿",
                                      role:"draft_email"}],
                          metadata:{agent_id:"...", status:"completed", ...}}

# emma 用文件名告诉用户（不发 artifact_id）
← message.start          {message:{id:"msg_2"}}
← content.start          {index:0, content_block:{type:"text"}}
← content.delta          {index:0, delta:{type:"text_delta",
                          text:"邮件已经准备好了：intern-schedule-email.md，正式商务语气，约 200 字。需要我先念一下要点，还是直接用？"}}
← content.stop           {index:0}
← message.delta          {delta:{stop_reason:"end_turn"}}
← message.stop
← task.end               {status:"success", num_turns:2}
```

**前端实现要点**：

1. **trace 级产出锚点**：监听 `tool.end.tool_name == "Specialists"` 或 `"Task"`，读 `artifacts` 字段，渲染"本次请求最终产出"卡片组。
2. **实时进度点亮**：监听 `subagent.event(tool_end)` 中 `payload.artifacts != null`，在对应 sub-agent 的折叠面板里增量出现单个 artifact 卡。
3. **任务级聚合**：监听 `subagent.end.artifacts`，在 sub-agent 卡片关闭时统一展示该 sub-agent 的全部产物。
4. **失败可视化**：`subagent.event(tool_end)` 中 `tool_name=SubmitTaskResult` 且 `is_error=true` → 黄色 / 红色警示，显示 `output`（含具体校验失败原因）。
5. **不在用户文本里出现 artifact_id**：emma 的 `content.delta` 文本应该用 ArtifactRef.name 引用产物。如果出现 `art_xxx` 字样，说明 prompt 配置有问题。

---

## 8. Error Handling

### 8.1 错误级别

| 级别 | 描述 | 连接状态 |
|------|------|---------|
| 事件级错误 | 单次请求失败，通过 `error` 事件通知 | 连接保持 |
| 流中断错误 | LLM 流式输出中断，以 `message.delta` + `message.stop` 结束当前消息 | 连接保持 |
| 连接级错误 | 协议违规、认证失败等，服务端关闭 WebSocket | 连接关闭 |

### 8.2 客户端重试策略

| error.type | 重试 | 策略 |
|------------|------|------|
| `rate_limit_error` | 是 | 等待 `retry_after_ms`，然后重试 |
| `overloaded_error` | 是 | 指数退避（1s, 2s, 4s, ...，最大 60s） |
| `internal_error` | 是 | 指数退避，最多重试 3 次 |
| `authentication_error` | 否 | 刷新凭证后重新连接 |
| `invalid_request_error` | 否 | 修正请求内容 |
| `permission_error` | 否 | 检查权限配置 |

### 8.3 连接断开重连

客户端应实现自动重连：

1. 记录最后收到的 `session_id`
2. 使用指数退避重连（初始 1s，最大 30s）
3. 重连成功后发送 `session.create`，传入原 `session_id` 以恢复会话上下文
4. 收到 `session.created` 后继续正常通信

---

## 9. Configuration

### 9.1 服务端配置

| 配置项 | 默认值 | 说明 |
|-------|--------|------|
| `channels.websocket.enabled` | `true` | 是否启用 WebSocket Channel |
| `channels.websocket.host` | `0.0.0.0` | 监听地址 |
| `channels.websocket.port` | `8081` | 监听端口 |
| `channels.websocket.path` | `/v1/ws` | 端点路径 |
| `channels.websocket.write_buffer` | `256` | 每连接写缓冲区大小（消息数） |
| `channels.websocket.ping_interval` | `30s` | Ping 间隔 |
| `channels.websocket.write_timeout` | `10s` | 单次写超时 |
| `channels.websocket.max_message_size` | `524288` | 最大入站帧大小（512KB） |
| `channels.websocket.tool_timeout` | `120s` | 客户端工具执行超时 |

### 9.2 背压机制

写缓冲区满时消息被丢弃（非阻塞 try-send），不阻塞 engine goroutine。客户端如果处理速度跟不上流式输出，可能丢失部分 delta 事件。建议客户端：
- 使用 `message.start` 中的 `message.id` 检测是否丢失消息
- 丢失消息时可通过 REST API 获取完整消息历史

### 9.3 环境变量覆盖

所有配置项均可通过环境变量覆盖，前缀 `CLAUDE_`，层级用 `_` 分隔：

```bash
CLAUDE_CHANNELS_WEBSOCKET_PORT=9091
CLAUDE_CHANNELS_WEBSOCKET_PATH=/v1/ws
CLAUDE_CHANNELS_WEBSOCKET_TOOL_TIMEOUT=180s
```

---

## 10. Complete Type Reference

### 10.1 Client Event Types

| type | 说明 | 状态 |
|------|------|------|
| `session.create` | 初始化会话（连接后首条消息） | **v1.4 新增** |
| `user.message` | 发送用户消息（v1.5 起支持多类型内容块） | 已实现 |
| `tool.result` | 上报工具执行结果 | **v1.1 新增** |
| `tool.progress` | 工具执行心跳（客户端→服务端） | **v1.1 新增** |
| `permission.response` | 回复权限审批请求 | **v1.3 新增**，v1.4 增加 `scope` 字段 |
| `session.update` | 更新会话配置 | 预留 |
| `session.interrupt` | 中断执行 | 已实现 |
| `session.resume` | 重连后请求续传 trace 事件 | **v1.11 新增**（协议稳定，server 端留存待实现） |
| `ping` | 心跳 | 已实现 |

### 10.2 Server Event Types

| type | 说明 | 状态 |
|------|------|------|
| `session.created` | 会话初始化完成（响应 `session.create`） | 已实现 |
| `session.updated` | 配置变更确认 | 预留 |
| `message.start` | 消息开始（每次 LLM 调用） | 已实现 |
| `content.start` | 内容块开始 | 已实现 |
| `content.delta` | 内容块增量 | 已实现 |
| `content.stop` | 内容块结束 | 已实现 |
| `message.delta` | 消息元数据（stop_reason + usage） | 已实现 |
| `message.stop` | 消息完成 | 已实现 |
| `tool.call` | 下发工具调用请求（客户端执行） | **v1.1 新增** |
| `tool.start` | 服务端工具执行开始 | **v1.2 新增** |
| `tool.end` | 服务端工具执行完成（v1.9 起含 render hints；v1.13 起含 `artifacts: []ArtifactRef`） | **v1.2 新增**，v1.9 / v1.13 增强 |
| `permission.request` | 请求权限审批（服务端→客户端） | **v1.3 新增** |
| `subagent.start` | 同步子 Agent 开始执行 | **v1.6 新增** |
| `subagent.event` | 子 Agent 实时流式事件（文本/工具执行；v1.13 起 tool_end 携带 artifacts） | **v1.8 新增**，v1.13 增强 |
| `subagent.end` | 同步子 Agent 执行完成（v1.13 起含 `artifacts: []ArtifactRef`） | **v1.6 新增**，v1.13 增强 |
| `agent.routed` | @-mention 路由通知 | **v1.7 新增** |
| `task.created` | 任务创建 | **v1.7 新增** |
| `task.updated` | 任务状态变更 | **v1.7 新增** |
| `agent.message` | Agent 间消息通知 | **v1.7 新增** |
| `agent.spawned` | 异步 Agent 启动 | **v1.7 新增** |
| `agent.idle` | Agent 进入空闲 | **v1.7 新增** |
| `agent.completed` | 异步 Agent 完成 | **v1.7 新增** |
| `agent.failed` | 异步 Agent 失败 | **v1.7 新增** |
| `team.created` | 团队创建 | **v1.7 新增** |
| `team.member_join` | 成员加入团队 | **v1.7 新增** |
| `team.member_left` | 成员离开团队 | **v1.7 新增** |
| `team.deleted` | 团队解散 | **v1.7 新增** |
| `task.end` | query-loop 任务结束 | 已实现（原 `result`） |
| `trace.started` | 请求开始（携带 envelope） | **v1.11 新增** |
| `trace.finished` | 请求成功结束（携带 metrics） | **v1.11 新增** |
| `trace.failed` | 请求失败 | **v1.11 新增** |
| `plan.created` | orchestrator 完成任务拆分 | **v1.11 新增** |
| `plan.updated` | 任务图被重规划 | **v1.11 新增** |
| `plan.completed` | 所有步骤进入终态（≥1 成功） | **v1.11 新增** |
| `plan.failed` | plan 整体失败（无步骤成功） | **v1.11 新增** |
| `step.dispatched` | orchestrator 派发了步骤到 worker | **v1.11 新增** |
| `step.started` | worker 实际开始执行 | **v1.11 新增** |
| `step.progress` | 步骤进度更新（节流） | **v1.11 新增** |
| `step.completed` | 步骤成功完成 | **v1.11 新增** |
| `step.failed` | 步骤失败（含 orphan_timeout 兜底） | **v1.11 新增** |
| `step.skipped` | 步骤因依赖失败被跳过 | **v1.11 新增** |
| `agent.heartbeat` | 长任务存活心跳 | **v1.11 新增** |
| `session.resumed` | 续传成功，准备补发 | **v1.11 新增** |
| `session.resume_failed` | 续传不可行 | **v1.11 新增** |
| `error` | 错误通知 | 已实现 |
| `pong` | 心跳响应 | 已实现 |

### 10.3 Content Block Types

| type | 说明 | delta type | 状态 |
|------|------|-----------|------|
| `text` | 文本 | `text_delta` | 已实现 |
| `tool_use` | 工具调用 | `input_json_delta` | 已实现 |
| `thinking` | 扩展思考 | `thinking_delta` | 预留 |
| `server_tool_use` | 服务端工具 | `input_json_delta` | 预留 |

### 10.4 Tool Result Status

| status | 说明 |
|--------|------|
| `success` | 工具执行成功 |
| `error` | 工具执行报错 |
| `denied` | 安全策略拒绝 |
| `timeout` | 执行超时 |
| `cancelled` | 用户取消 |

### 10.5 Usage Object

```json
{
  "input_tokens": 150,
  "output_tokens": 42,
  "cache_read_tokens": 100,
  "cache_write_tokens": 0
}
```

出现位置：
- `message.start.message.usage` — input tokens（output 为 0）
- `message.delta.usage` — output tokens
- `task.end.total_usage` — query-loop 累计

### 10.6 ArtifactRef Object（v1.13+）

跨 Agent 共享数据的引用句柄。出现在三处事件上：

- `tool.end.artifacts` — 单个 ArtifactWrite 产出 / Specialists 或 Task 工具的聚合产出
- `subagent.end.artifacts` — 一个 sub-agent 任务全部产物的聚合视图
- `subagent.event.payload.artifacts` — sub-agent 内 ArtifactWrite / SubmitTaskResult 完成后的实时 Ref

```json
{
  "artifact_id": "art_a1b2c3d4e5f6789012345678",
  "name": "intern-schedule-email.md",
  "type": "file",
  "mime_type": "text/markdown",
  "size_bytes": 1240,
  "description": "实习生作息安排邮件正稿",
  "preview_text": "尊敬的张老师，关于本期实习生的工作作息...",
  "uri": "artifact://art_a1b2c3d4e5f6789012345678",
  "role": "draft_email"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `artifact_id` | string | 框架生成的稳定 ID（`art_<24 hex>`）。**LLM 不可伪造**——服务端在 ArtifactWrite 时分配 |
| `name` | string | 人类可读名称（`q4-report.md` / `sales-data.csv`）。**面向用户的标签用这个，不要发 ID** |
| `type` | string | `"structured"` / `"file"` / `"blob"` —— 决定客户端是 inline 渲染还是 download 入口 |
| `mime_type` | string | 进一步缩小渲染选择（`"text/markdown"` / `"application/json"` / `"text/csv"`）|
| `size_bytes` | int | 内容字节长度。客户端用它决定要不要 inline-preview |
| `description` | string | producer 给的一行说明。"这是什么"——可作为卡片副标题 |
| `preview_text` | string | UTF-8 安全的截断预览（≤512B）。`tool.end` 上不一定每个 Ref 都填 |
| `uri` | string | 形如 `artifact://art_xxx`。客户端用 ArtifactRead(mode=full) 拉全文 |
| `role` | string | 契约模式下该产物对应的 `expected_outputs[].role`。无契约时为空 |

**渲染建议**：

- 卡片标题用 `name`，副标题用 `description`，`preview_text` 折叠展开
- `role` 仅在 dev tools / 调试视图展示（用户不关心 role 字段）
- `artifact_id` 永远不出现在面向用户的 UI 文本（仅在 url、event-id 关联场景使用）

### 10.7 ExpectedOutput Object（v1.13+）

派发 sub-agent 时的产出契约，仅出现在 **客户端→服务端** 的 `Task` 工具入参里。一个 task 可声明多个 `expected_outputs`：

```json
{
  "role": "comparison_table",
  "type": "structured",
  "mime_type": "application/json",
  "schema": {"type": "table", "columns": ["metric", "2023", "2024", "yoy"]},
  "min_size_bytes": 100,
  "required": true,
  "acceptance_criteria": "包含每月销量同比对比，YoY 字段为百分比"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `role` | string | ✅ | 契约级别的产物名（`"draft_email"` / `"findings_report"`），sub-agent 在 SubmitTaskResult 时回填这个 role 标记每份 artifact |
| `type` | string | ❌ | 限定 artifact 类型 `"structured"` / `"file"` / `"blob"`。空 = 任意类型 |
| `mime_type` | string | ❌ | 进一步缩小约束 |
| `schema` | object | ❌ | 结构化产物的形状声明（列定义 / JSON-Schema 片段）。M6 质量检查用 |
| `min_size_bytes` | int | ❌ | 最小字节数（默认 1）——挡 placeholder / 空内容提交 |
| `required` | bool | ❌ | true 时 sub-agent 必须提交。SubmitTaskResult schema 的 `artifacts.minItems` 隐式由 required 数量决定 |
| `acceptance_criteria` | string | ❌ | 自由文本质量描述。Milestone B 的 LLM-as-judge 用，目前仅信息字段 |

**校验流程**（M3+M4）：

1. sub-agent 写 N 份 artifact → 每份获得 `art_xxx`
2. sub-agent 调 `SubmitTaskResult` 提交 `[{role, artifact_id}, ...]` + `summary`
3. 框架按 ExpectedOutputs 校验：每个 `required: true` 的 role 必须有对应 artifact_id；ID 必须存在、size>0、type/mime 匹配、producer.task_id 与本任务一致、created_at ≥ 任务开始时间
4. 校验失败 → 返回结构化错误（哪条不合规），sub-agent 改写后重新提交（≤3 次）

**ExpectedOutput 不出现在服务端→客户端的事件上**——它是契约的输入面，输出面通过 ArtifactRef + role 字段反向呈现（哪个 role 对应哪个 artifact_id）。

---

## 11. Multi-Agent Architecture Notes

本节记录多 Agent 体系的架构参考信息，作为协议实现的补充说明。

### 11.1 事件分层

| Phase | 命名空间 | 事件 | 说明 | 状态 |
|-------|---------|------|------|------|
| 1 | `subagent.*` | `subagent.start`, `subagent.event`, `subagent.end` | 同步子 Agent 生命周期 + 实时流式 | **v1.6 已实现，v1.8 新增 event** |
| 1.5 | `agent.routed` | `agent.routed` | @-mention 路由通知 | **v1.7 已实现** |
| 2 | `task.*` | `task.created`, `task.updated` | 任务系统状态变更 | **v1.7 已实现** |
| 3 | `agent.message` | `agent.message` | Agent 间通信可见性 | **v1.7 已实现** |
| 4 | `agent.*` | `agent.spawned`, `agent.idle`, `agent.completed`, `agent.failed` | 异步 Agent 完整生命周期 | **v1.7 已实现** |
| 5 | `team.*` | `team.created`, `team.member_join`, `team.member_left`, `team.deleted` | 团队编排 | **v1.7 已实现** |
| 6 | `trace.*` / `plan.*` / `step.*` / `agent.heartbeat` / `session.resume*` | 14 种带 envelope 的生命周期事件 + 续传协议 | 结构化 Emit 协议 | **v1.11 已实现**（留存缓冲为后续工作） |

### 11.2 Capabilities 字段完整列表

| 字段 | 说明 | 引入版本 |
|------|------|---------|
| `streaming` | 支持流式输出 | v1.0 |
| `tools` | 支持工具调用 | v1.0 |
| `client_tools` | 启用客户端工具执行模式 | v1.1 |
| `thinking` | 支持扩展思考 | v1.0（预留） |
| `multi_turn` | 支持多轮 query-loop | v1.0 |
| `image_input` | 支持图片输入 | v1.5 |
| `sub_agents` | 支持子 Agent 生成 | v1.6 |
| `tasks` | 支持任务系统 | v1.7 |
| `messaging` | 支持 Agent 间通信 | v1.7 |
| `async_agent` | 支持异步 Agent | v1.7 |
| `teams` | 支持团队模式 | v1.7 |
| `emit` | 启用结构化 Emit 协议（envelope/display/metrics + trace/plan/task/heartbeat 事件） | v1.11 |

### 11.3 客户端渲染建议

| 事件 | 推荐 UI 表现 |
|------|------------|
| `subagent.start` | 显示 spinner + Agent 描述 |
| `subagent.event` | 实时渲染子 Agent 文本输出、显示工具执行状态 |
| `subagent.end` | spinner 结束，显示耗时和状态 |
| `task.created` | 任务列表新增项（待办状态） |
| `task.updated` | 任务状态颜色变化 + spinner（active_form 文案） |
| `agent.message` | 消息气泡 / 时间线条目 |
| `agent.spawned` | 新的 Agent 卡片出现 |
| `agent.idle` | Agent 卡片变灰 / 等待态 |
| `agent.completed` | Agent 卡片打钩 |
| `agent.failed` | Agent 卡片标红 |
| `team.created` | 团队面板出现 |
| `team.member_join` | 团队面板新增成员 |
| `team.deleted` | 团队面板收起 / 消失 |

---

## Appendix A: Quick Start

### wscat

```bash
npm i -g wscat
wscat -c "ws://localhost:8081/v1/ws"

# 先发送 session.create 初始化会话
> {"type":"session.create","event_id":"c0","session_id":"test1"}

# 等待 session.created 后发送（文本快捷方式）
> {"type":"user.message","event_id":"c1","text":"hello"}

# 或发送多类型内容（v1.5）
> {"type":"user.message","event_id":"c2","content":[{"type":"text","text":"describe this"},{"type":"image","source":{"type":"path","path":"/tmp/img.png"}}]}
```

### Python

```python
import json, asyncio, websockets

async def main():
    uri = "ws://localhost:8081/v1/ws"
    async with websockets.connect(uri) as ws:
        # Send session.create to initialize
        await ws.send(json.dumps({
            "type": "session.create",
            "event_id": "c0",
            "session_id": "test1"
        }))

        # Wait for session.created
        init = json.loads(await ws.recv())
        print(f"[session.created] {init['session_id']}")
        print(f"  protocol: {init.get('protocol_version')}")

        # Send message
        await ws.send(json.dumps({
            "type": "user.message",
            "event_id": "c1",
            "content": {"type": "text", "text": "What is 1+1?"}
        }))

        # Read streaming events
        while True:
            raw = json.loads(await ws.recv())
            t = raw["type"]

            if t == "content.delta":
                delta = raw["delta"]
                if delta["type"] == "text_delta":
                    print(delta["text"], end="", flush=True)
            elif t == "message.delta":
                print(f"\n[message.delta] stop_reason={raw['delta']['stop_reason']}")
            elif t == "tool.call":
                # Client-side tool execution
                print(f"\n[tool.call] {raw['tool_name']}: {raw['input']}")
                # Execute locally, then send result
                await ws.send(json.dumps({
                    "type": "tool.result",
                    "event_id": "tr1",
                    "session_id": init["session_id"],
                    "tool_use_id": raw["tool_use_id"],
                    "status": "success",
                    "output": "command output here"
                }))
            elif t == "permission.request":
                # Server requests permission approval for a tool
                print(f"\n[permission.request] {raw['tool_name']}: {raw['message']}")
                print(f"  Input: {raw['tool_input']}")
                for opt in raw.get("options", []):
                    print(f"  [{opt['label']}] scope={opt['scope']} allow={opt['allow']}")
                # Auto-approve with session scope for demo (real client should ask user)
                await ws.send(json.dumps({
                    "type": "permission.response",
                    "event_id": "pr1",
                    "session_id": init["session_id"],
                    "request_id": raw["request_id"],
                    "approved": True,
                    "scope": "session"
                }))
            elif t == "subagent.start":
                print(f"\n[subagent.start] {raw.get('agent_name','')}: "
                      f"{raw.get('description','')}")
            elif t == "subagent.event":
                payload = raw.get("payload", {})
                et = payload.get("event_type", "")
                if et == "text":
                    print(f"  [subagent.text] {payload.get('text','')}", end="")
                elif et == "tool_start":
                    print(f"  [subagent.tool_start] {payload.get('tool_name','')}")
                elif et == "tool_end":
                    status = "error" if payload.get("is_error") else "ok"
                    print(f"  [subagent.tool_end] {payload.get('tool_name','')} "
                          f"status={status}")
            elif t == "subagent.end":
                print(f"\n[subagent.end] {raw.get('agent_name','')} "
                      f"status={raw.get('status','')} "
                      f"duration={raw.get('duration_ms',0)}ms")
            elif t == "agent.routed":
                print(f"\n[agent.routed] @{raw.get('agent_name','')}: "
                      f"{raw.get('description','')}")
            elif t == "task.created":
                task = raw.get("task", {})
                print(f"\n[task.created] #{task.get('task_id')}: "
                      f"{task.get('subject')}")
            elif t == "task.updated":
                task = raw.get("task", {})
                print(f"[task.updated] #{task.get('task_id')} "
                      f"status={task.get('status')} "
                      f"owner={task.get('owner','')}")
            elif t == "agent.message":
                msg_data = raw.get("message", {})
                print(f"[agent.message] {msg_data.get('from')} → "
                      f"{msg_data.get('to')}: {msg_data.get('summary','')}")
            elif t == "agent.spawned":
                print(f"[agent.spawned] {raw.get('agent_name','')}: "
                      f"{raw.get('description','')}")
            elif t == "agent.completed":
                print(f"[agent.completed] {raw.get('agent_name','')} "
                      f"duration={raw.get('duration_ms',0)}ms")
            elif t == "agent.failed":
                err = raw.get("error", {})
                print(f"[agent.failed] {raw.get('agent_name','')}: "
                      f"{err.get('message','')}")
            elif t in ("team.created", "team.member_join",
                       "team.member_left", "team.deleted"):
                team = raw.get("team", {})
                print(f"[{t}] {team.get('team_name','')} "
                      f"members={team.get('members',[])}")
            elif t == "task.end":
                print(f"[task.end] status={raw['status']} "
                      f"turns={raw['num_turns']} "
                      f"duration={raw['duration_ms']}ms")
                break
            elif t == "error":
                err = raw["error"]
                print(f"[error] {err['type']}: {err['message']}")
                break

asyncio.run(main())
```

### Go

```go
import "nhooyr.io/websocket"

ctx := context.Background()
ws, _, _ := websocket.Dial(ctx, "ws://localhost:8081/v1/ws", nil)
defer ws.Close(websocket.StatusNormalClosure, "done")

// Send session.create
create := `{"type":"session.create","event_id":"c0","session_id":"test1"}`
ws.Write(ctx, websocket.MessageText, []byte(create))

// Read session.created
_, data, _ := ws.Read(ctx)
fmt.Println("init:", string(data))

// Send message
msg := `{"type":"user.message","event_id":"c1","content":{"type":"text","text":"hello"}}`
ws.Write(ctx, websocket.MessageText, []byte(msg))

// Read events until task.end
for {
    _, data, err := ws.Read(ctx)
    if err != nil { break }
    var evt map[string]any
    json.Unmarshal(data, &evt)
    switch evt["type"] {
    case "tool.call":
        // Execute tool locally, send result
        result := fmt.Sprintf(`{"type":"tool.result","event_id":"tr1","session_id":"test1","tool_use_id":"%s","status":"success","output":"..."}`, evt["tool_use_id"])
        ws.Write(ctx, websocket.MessageText, []byte(result))
    case "permission.request":
        // Approve permission request with session scope (real client should ask user)
        resp := fmt.Sprintf(`{"type":"permission.response","event_id":"pr1","session_id":"test1","request_id":"%s","approved":true,"scope":"session"}`, evt["request_id"])
        ws.Write(ctx, websocket.MessageText, []byte(resp))
    case "subagent.start":
        fmt.Printf("[subagent.start] %s: %s\n", evt["agent_name"], evt["description"])
    case "subagent.event":
        payload := evt["payload"].(map[string]any)
        switch payload["event_type"] {
        case "text":
            fmt.Printf("  [subagent.text] %s", payload["text"])
        case "tool_start":
            fmt.Printf("  [subagent.tool_start] %s\n", payload["tool_name"])
        case "tool_end":
            fmt.Printf("  [subagent.tool_end] %s is_error=%v\n", payload["tool_name"], payload["is_error"])
        }
    case "subagent.end":
        fmt.Printf("[subagent.end] %s status=%s duration=%vms\n", evt["agent_name"], evt["status"], evt["duration_ms"])
    case "agent.routed":
        fmt.Printf("[agent.routed] @%s: %s\n", evt["agent_name"], evt["description"])
    case "task.created", "task.updated":
        task := evt["task"].(map[string]any)
        fmt.Printf("[%s] #%s: %s status=%s\n", evt["type"], task["task_id"], task["subject"], task["status"])
    case "agent.message":
        msg := evt["message"].(map[string]any)
        fmt.Printf("[agent.message] %s → %s: %s\n", msg["from"], msg["to"], msg["summary"])
    case "agent.spawned":
        fmt.Printf("[agent.spawned] %s: %s\n", evt["agent_name"], evt["description"])
    case "agent.completed":
        fmt.Printf("[agent.completed] %s duration=%vms\n", evt["agent_name"], evt["duration_ms"])
    case "agent.failed":
        errInfo := evt["error"].(map[string]any)
        fmt.Printf("[agent.failed] %s: %s\n", evt["agent_name"], errInfo["message"])
    case "team.created", "team.member_join", "team.member_left", "team.deleted":
        team := evt["team"].(map[string]any)
        fmt.Printf("[%s] %v members=%v\n", evt["type"], team["team_name"], team["members"])
    case "task.end":
        fmt.Println("done:", string(data))
        return
    }
}
```
