# Changelog

| 版本 | 日期 | 变更 |
|------|------|------|
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

# WebSocket Channel Protocol v1.9

## 1. Overview

本协议定义了 harnessclaw-go WebSocket Channel 的双向通信规范。客户端通过 WebSocket 发送用户消息和控制指令，服务端以流式事件实时推送 query-loop 的完整执行过程。

**协议版本**: `1.9`

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
  "protocol_version": "1.9",
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
      "teams": true
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
| `agent` | Agent | 子 Agent 输出摘要 |
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
  "agent_name": "explorer",
  "description": "Searching authentication module",
  "agent_type": "sync",
  "parent_agent_id": "main"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `agent_id` | string | 子 Agent 唯一 ID |
| `agent_name` | string | Agent 标识名（由 LLM 在 Agent 工具 `name` 参数中指定，可选） |
| `description` | string | 人类可读描述（由 LLM 在 Agent 工具 `description` 参数中指定） |
| `agent_type` | string | Agent 类型。Phase 1 固定为 `"sync"` |
| `parent_agent_id` | string | 父 Agent ID。顶层 query-loop 为 `"main"` |

#### `subagent.end` — 子 Agent 执行完成

```json
{
  "type": "subagent.end",
  "event_id": "evt_i9j0k1l2",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "explorer",
  "status": "completed",
  "duration_ms": 12300,
  "num_turns": 3,
  "usage": {
    "input_tokens": 15000,
    "output_tokens": 3200,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0
  },
  "denied_tools": []
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

**status 枚举**:

| status | 说明 |
|--------|------|
| `completed` | 正常完成（LLM stop_reason 为 `end_turn`） |
| `max_turns` | 达到最大轮次限制 |
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

子 Agent 执行过程中，服务端将其内部的文本输出和工具执行事件实时包装为 `subagent.event` 转发给客户端。此事件嵌套在 `subagent.start` 和 `subagent.end` 之间，客户端可据此渐进渲染子 Agent 的工作过程。

**文本输出**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_001",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "explorer",
  "payload": {
    "event_type": "text",
    "text": "Found authentication module at internal/auth/"
  }
}
```

**工具开始执行**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_002",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "explorer",
  "payload": {
    "event_type": "tool_start",
    "tool_name": "Grep",
    "tool_use_id": "toolu_sub_1",
    "tool_input": "{\"pattern\":\"func Auth\",\"path\":\"internal/auth/\"}"
  }
}
```

**工具执行完成**:

```json
{
  "type": "subagent.event",
  "event_id": "evt_sa_003",
  "session_id": "sess_abc123",
  "agent_id": "sess_abc123_sub_e5f6g7h8",
  "agent_name": "explorer",
  "payload": {
    "event_type": "tool_end",
    "tool_name": "Grep",
    "tool_use_id": "toolu_sub_1",
    "output": "internal/auth/handler.go:15:func AuthMiddleware(...)",
    "is_error": false
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
| `event_type` | string | 是 | 内部事件类型：`"text"`、`"tool_start"`、`"tool_end"` |
| `text` | string | 否 | 文本内容（`event_type: "text"` 时） |
| `tool_name` | string | 否 | 工具名（`event_type: "tool_start"` / `"tool_end"` 时） |
| `tool_use_id` | string | 否 | 工具调用 ID（`event_type: "tool_start"` / `"tool_end"` 时） |
| `tool_input` | string | 否 | 工具输入 JSON 字符串（`event_type: "tool_start"` 时） |
| `output` | string | 否 | 工具输出内容（`event_type: "tool_end"` 时） |
| `is_error` | bool | 否 | 工具执行是否出错（`event_type: "tool_end"` 时） |

**客户端处理建议**:
- `event_type: "text"` — 将文本追加到子 Agent 的输出区域，实现逐字渲染
- `event_type: "tool_start"` — 显示工具执行指示器（如 "Running Grep..."）
- `event_type: "tool_end"` — 更新工具执行状态为完成/失败，可选展示工具输出摘要
- 使用 `agent_id` 将事件关联到对应的子 Agent UI 组件

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

### 7.13 Extended Thinking（预留）

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
| `tool.end` | 服务端工具执行完成（v1.9 起含 render hints） | **v1.2 新增**，v1.9 增强 |
| `permission.request` | 请求权限审批（服务端→客户端） | **v1.3 新增** |
| `subagent.start` | 同步子 Agent 开始执行 | **v1.6 新增** |
| `subagent.event` | 子 Agent 实时流式事件（文本/工具执行） | **v1.8 新增** |
| `subagent.end` | 同步子 Agent 执行完成 | **v1.6 新增** |
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
