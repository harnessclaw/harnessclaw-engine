# Changelog

| 版本 | 日期 | 变更 |
|------|------|------|
| 1.4 | 2026-04-08 | 连接协议改为显式握手：客户端必须发送 `session.create` 后才能收到 `session.created`，连接建立时服务端不再自动推送；新增 pre-init gate，初始化前仅接受 `session.create` 和 `ping`；权限审批增强为三选项模型（单次允许/会话级允许/拒绝）；会话级审批粒度为「程序 + 子命令」（如 `Bash:git push`），而非工具名或程序名；`permission.request` 新增 `options` + `permission_key` 字段；`permission.response` 新增 `scope` 字段；移除权限审批超时（无限等待直到用户操作） |
| 1.3 | 2026-04-08 | 新增权限审批协议（`permission.request`/`permission.response`）；服务端工具执行需要用户确认时，通过 WebSocket 异步审批而非直接拒绝 |
| 1.2 | 2026-04-07 | 新增服务端工具执行事件（`tool.start`/`tool.end`）；LLM tool_use 输出统一走 `content.*` 内容块；`EngineEventToolUse` 与 `EngineEventToolStart`/`EngineEventToolEnd` 职责分离 |
| 1.1 | 2026-04-07 | **Breaking**: 1) 新增客户端工具执行协议（`tool.call`/`tool.result`）；2) 工具安全管控协议（denied/timeout/cancelled）；3) `result` → `task.end`；4) `type` 字段统一为 `dot.notation` 风格（`message.start`/`content.start`/`content.delta`/`content.stop`/`message.delta`/`message.stop`） |
| 1.0 | 2026-04-07 | 初始版本。对齐 Anthropic streaming + OpenAI Realtime 协议设计 |

---

# WebSocket Channel Protocol v1.4

## 1. Overview

本协议定义了 harnessclaw-go WebSocket Channel 的双向通信规范。客户端通过 WebSocket 发送用户消息和控制指令，服务端以流式事件实时推送 query-loop 的完整执行过程。

**协议版本**: `1.4`

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
- 工具执行模型参考 Claude Code CLI 的本地工具调用机制

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

```json
{
  "type": "user.message",
  "event_id": "evt_client_001",
  "session_id": "sess_abc123",
  "content": {
    "type": "text",
    "text": "What is 1+1?"
  }
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `content` | object | 是 | 消息内容 |
| `content.type` | string | 是 | `"text"` / `"image"` / `"file"` (预留) |
| `content.text` | string | 条件 | `type: "text"` 时必填 |

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
  "protocol_version": "1.4",
  "session": {
    "model": "claude-sonnet-4-20250514",
    "capabilities": {
      "streaming": true,
      "tools": true,
      "client_tools": true,
      "thinking": false,
      "multi_turn": true,
      "image_input": false
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
  "metadata": {
    "exit_code": 0,
    "duration_ms": 50
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
| `metadata` | object | 工具特定的元数据，可选（如 `exit_code`） |

**`status` 与 `is_error` 的区别**:
- `status` 表示执行层面的状态：成功完成为 `"success"`，出错为 `"error"`
- `is_error` 表示工具结果是否应作为错误传递给 LLM（与 Anthropic API `tool_result.is_error` 对齐）
- 通常两者一致，但某些场景下 `status: "success"` 且 `is_error: true`（如命令返回非零 exit code 但执行本身未出错）

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

### 6.6 Error Event

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
  |       {protocol_version:"1.4",session:{...}}  |
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

### 7.9 Extended Thinking（预留）

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
| `user.message` | 发送用户消息 | 已实现 |
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
| `tool.end` | 服务端工具执行完成 | **v1.2 新增** |
| `permission.request` | 请求权限审批（服务端→客户端） | **v1.3 新增** |
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

## Appendix A: Quick Start

### wscat

```bash
npm i -g wscat
wscat -c "ws://localhost:8081/v1/ws"

# 先发送 session.create 初始化会话
> {"type":"session.create","event_id":"c0","session_id":"test1"}

# 等待 session.created 后发送
> {"type":"user.message","event_id":"c1","content":{"type":"text","text":"hello"}}
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
    case "task.end":
        fmt.Println("done:", string(data))
        return
    }
}
```
