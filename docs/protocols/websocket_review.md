# WebSocket Protocol Review

> 评审对象：`go_rebuild/docs/protocols/websocket.md`
> 参照基线：Anthropic Messages Streaming API、OpenAI Realtime WebSocket API
> 评审维度：可读性、可扩展性、与主流协议的对齐度

---

## 一、与主流协议的关键差异对照

### 1.1 事件信封结构

| 维度 | Anthropic (SSE) | OpenAI Realtime (WS) | 当前协议 |
|------|-----------------|---------------------|---------|
| 顶层类型 | `event: content_block_delta` | `"type": "response.text.delta"` | `"type": "stream_event"` + `"event.type": "content_block_delta"` |
| 事件 ID | SSE `id:` 字段 | `event_id` (每条消息) | `uuid` (每条消息) |
| 请求关联 | 无（HTTP 请求级） | `event_id` 回引客户端事件 | **缺失** |

**问题：`stream_event` 嵌套多余一层。**

Anthropic 和 OpenAI 都把事件类型放在顶层。当前协议用 `type: "stream_event"` 包裹了一层，导致客户端要解两层才能拿到实际事件类型：

```json
// 当前 — 2 层
{"type": "stream_event", "event": {"type": "content_block_delta", "delta": {...}}}

// Anthropic 风格 — 1 层
{"type": "content_block_delta", "index": 0, "delta": {...}}
```

**建议**：去掉 `stream_event` 包装层，将 `content_block_start`、`content_block_delta`、`content_block_stop` 提升为顶层 `type`。这与 Anthropic 的 SSE 事件完全对齐，客户端直接 `switch msg.type` 即可。

### 1.2 缺少消息级生命周期事件

Anthropic 的流式序列：

```
message_start          ← 携带 message.id, model, input usage
  content_block_start
  content_block_delta  (N 条)
  content_block_stop
message_delta          ← 携带 stop_reason, output usage
message_stop           ← 流结束标记
```

OpenAI 的流式序列：

```
response.created       ← 携带 response_id
  response.output_item.added
    response.content_part.added
    response.text.delta  (N 条)
    response.content_part.done
  response.output_item.done
response.done          ← 携带 status, usage
```

当前协议的序列：

```
(无 message_start)
  content_block_start
  content_block_delta  (N 条)
  content_block_stop
(无 message_delta)
result                 ← 混合了 stop_reason + usage + duration
```

**缺失项及影响**：

| 缺失事件 | 影响 |
|---------|------|
| `message_start` | 客户端无法获取 message_id、model 名称、input token 数（Anthropic 在此时报告） |
| `message_delta` | 无法在内容块结束后、流结束前传递 stop_reason 和 output usage |
| `message_stop` | 没有明确的流结束标记；`result` 承担了过多职责 |

**建议**：增加 `message_start` 和 `message_stop`。`result` 保留为 query-loop 级的轮次总结（这是我们特有的），但单次 LLM 调用的元数据应该走 `message_start`/`message_delta`。

### 1.3 错误结构过于简单

**Anthropic**：
```json
{"type": "error", "error": {"type": "authentication_error", "message": "invalid x-api-key"}}
```

**OpenAI**：
```json
{"type": "error", "error": {"type": "invalid_request_error", "message": "...", "code": "...", "param": "...", "event_id": "evt_..."}}
```

**当前协议**：
```json
{"type": "system", "subtype": "error", "message": "rate limit exceeded"}
```

**问题**：
- 错误没有独立 `type`，藏在 `system` 的 `subtype` 里
- 没有 `error_code`（机器可解析） — 客户端无法做自动化重试/降级判断
- 没有 `error_type` 分类（`authentication_error` / `rate_limit_error` / `overloaded_error` / `invalid_request_error`）
- 没有请求关联字段 — 无法知道错误是哪个请求触发的

**建议**：错误提升为独立 `type: "error"`，采用结构化 error 对象：

```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "message": "You have exceeded the rate limit",
    "request_id": "u1"
  }
}
```

### 1.4 `subtype` 模式不符合业界惯例

当前协议大量使用 `type` + `subtype` 二级分发：
- `system` + `subtype: init/status/error`
- `result` + `subtype: success/error_max_turns/error_model/...`
- `control_request` + `subtype: interrupt`

Anthropic 和 OpenAI 均使用**扁平化的具体 type 名称**：
- Anthropic: `message_start`, `message_stop`, `error` — 每种含义对应独立 type
- OpenAI: `session.created`, `response.done`, `error` — 使用点分命名空间

**问题**：`subtype` 增加了客户端两级 switch 的复杂度，且 `system` 这个 type 太宽泛。

**建议**：
- `system/init` → `session.created`
- `system/error` → `error`
- `system/status` → `session.updated`
- `result/success` → `result`（`status: "success"`）
- `control_request/interrupt` → `session.interrupt`

### 1.5 缺少协议版本

| 平台 | 版本机制 |
|------|---------|
| Anthropic | Header: `anthropic-version: 2023-06-01`，beta 特性: `anthropic-beta: interleaved-thinking-2025-05-14` |
| OpenAI | Subprotocol: `openai-beta.realtime-v1`，模型通过 URL query 指定 |
| 当前协议 | **无任何版本标识** |

**建议**：在连接 URL 或 `session.created` 消息中加入 `protocol_version`：

```
ws://host:port/ws/v1?session_id=xxx
```

或在 init 消息中返回：

```json
{"type": "session.created", "protocol_version": "1.0", "session_id": "xxx"}
```

### 1.6 请求-响应关联缺失

客户端发送 `{"type":"user","uuid":"u1","text":"hello"}`，之后收到的所有 server 事件都没有回引 `u1`。

**OpenAI 的做法**：每条 server 事件携带 `response_id`、`item_id`、`output_index`、`content_index`，可以精确关联到每个请求的每个内容块。错误事件通过 `error.event_id` 回指触发它的客户端事件。

**建议**：Server 事件增加 `request_id` 字段（对应客户端的 `uuid`），至少在 `message_start` 和 `error` 上携带。

---

## 二、可读性问题

### 2.1 文档暴露了内部实现细节

"EngineEvent → 协议消息映射" 这一节描述的是服务端内部的 mapper 逻辑。作为面向客户端的协议文档，不应包含此内容。客户端不需要知道 `EngineEvent` 是什么。

**建议**：移除此节或移入 `docs/internals/` 目录。客户端协议文档只需描述客户端看到的事件序列。

### 2.2 缺少认证说明

连接参数只提到了 `session_id` 和 `user_id`，没有提到认证方式。主流做法：
- OpenAI：`Authorization: Bearer <key>` 作为 HTTP 升级请求的 header，或 WebSocket subprotocol
- Anthropic：`x-api-key` header

**建议**：增加 Authentication 章节，即使当前是 placeholder 也应说明设计意向（query param token / header / subprotocol）。

### 2.3 文档结构可优化

建议按照协议文档标准结构重新组织：

```
1. Overview（概述 + 协议版本）
2. Authentication（认证）
3. Connection（连接 + 握手 + 断连）
4. Message Format（公共信封）
5. Client Events（客户端事件，逐个描述）
6. Server Events（服务端事件，逐个描述）
7. Event Sequences（完整交互序列图）
8. Error Handling（错误分类 + 重试策略）
9. Configuration（服务端配置项）
10. Appendix（测试示例、SDK 示例）
```

### 2.4 中英文混排

部分表格和说明混用中英文。面向开发者的协议文档建议统一用英文字段名 + 中文说明，或全英文。至少字段名、type 值、示例 JSON 中不应出现中文。

---

## 三、可扩展性问题

### 3.1 无扩展字段机制

Anthropic 和 OpenAI 的 JSON 响应都采用**开放结构**（允许未知字段，客户端忽略不识别的字段）。但我们的协议文档没有声明这一约定。

**建议**：在 Message Format 章节明确声明：
> 客户端 **MUST** 忽略不识别的字段和未知的 `type` 值。服务端 **MAY** 在任何消息中添加新字段。

### 3.2 缺少 thinking 块类型

Anthropic 的 `content_block_start` 支持 `type: "thinking"` 和 `type: "redacted_thinking"` 用于扩展思考功能。这是高价值特性，我们的 `content_block.type` 枚举应预留。

**建议**：在 `content_block.type` 枚举中增加 `thinking` 类型，为后续 extended thinking 留出空间。

### 3.3 缺少能力协商

OpenAI 在连接后立即发送 `session.created`，其中包含完整的 session 配置（支持的 modalities、tools、model 等）。客户端可以通过 `session.update` 修改配置。

当前协议的 `system/init` 只携带 `"connected"` 字符串，没有传递任何能力信息。

**建议**：`session.created` 中返回服务端能力：

```json
{
  "type": "session.created",
  "session_id": "...",
  "protocol_version": "1.0",
  "capabilities": {
    "streaming": true,
    "tools": true,
    "thinking": false,
    "multi_turn": true
  }
}
```

### 3.4 `user` 类型名称不够具体

`"type": "user"` 与角色概念（user/assistant/system）冲突。当需要支持发送图片、文件等内容时，`user` 这个 type 名无法区分不同的客户端操作。

OpenAI 使用具体的动词命名：`conversation.item.create`、`response.create`、`session.update`。

**建议**：改为 `user.message`（发送消息）或采用命名空间 `conversation.message.create`，为将来增加 `user.image`、`user.file` 等留空间。

### 3.5 `result` 混合了不同关注点

当前 `result` 同时承载：
- query-loop 元数据（`num_turns`, `duration_ms`）
- LLM 元数据（`stop_reason`, `usage`）
- 错误状态（`is_error`, `subtype`）

**建议**：拆分为：
- `message_delta` — 单次 LLM 调用结束：`stop_reason` + `usage`
- `message_stop` — 消息完成标记
- `result` — query-loop 轮次总结：`num_turns` + `duration_ms` + `status`

---

## 四、汇总建议（按优先级）

### P0 — 必须修复（协议正确性）

| # | 问题 | 建议 |
|---|------|------|
| 1 | 去掉 `stream_event` 嵌套 | `content_block_start/delta/stop` 提升为顶层 type |
| 2 | 增加 `message_start` / `message_stop` | 对齐 Anthropic 消息生命周期 |
| 3 | 错误独立为 `type: "error"` | 结构化 error 对象（type + code + message） |
| 4 | 增加协议版本 | URL path 或 init 消息中携带 |

### P1 — 强烈建议（可扩展性）

| # | 问题 | 建议 |
|---|------|------|
| 5 | 去掉 `subtype`，扁平化 type | `session.created`, `session.updated`, `error` |
| 6 | 增加请求关联 | Server 事件携带 `request_id` |
| 7 | 声明前向兼容约定 | 客户端 MUST 忽略未知字段/type |
| 8 | 预留 `thinking` 块类型 | `content_block.type` 枚举增加 `thinking` |
| 9 | `user` → `user.message` | 为多模态留空间 |

### P2 — 建议改进（可读性）

| # | 问题 | 建议 |
|---|------|------|
| 10 | 移除 EngineEvent 映射表 | 移入内部文档 |
| 11 | 增加 Authentication 章节 | 即使 placeholder 也应说明 |
| 12 | 重组文档结构 | 按标准协议文档结构 |
| 13 | `result` 职责拆分 | `message_delta` + `message_stop` + `result` |
| 14 | init 消息返回能力信息 | capabilities 对象 |

---

## 五、修改后的事件序列（参考）

```
← session.created       {session_id, protocol_version, capabilities}
→ user.message           {request_id, text}
← message_start          {message_id, model, request_id, usage: {input_tokens}}
← content_block_start    {index: 0, content_block: {type: "text"}}
← content_block_delta    {index: 0, delta: {type: "text_delta", text: "Hello"}}
← content_block_delta    {index: 0, delta: {type: "text_delta", text: " World"}}
← content_block_stop     {index: 0}
← message_delta           {delta: {stop_reason: "end_turn"}, usage: {output_tokens: 5}}
← message_stop
← result                  {status: "success", num_turns: 1, duration_ms: 856}
```

带工具调用：

```
→ user.message           {request_id: "r1", text: "列出文件"}
← message_start          {message_id: "msg_1", request_id: "r1"}
← content_block_start    {index: 0, content_block: {type: "text"}}
← content_block_delta    {index: 0, delta: {type: "text_delta", text: "我来查看"}}
← content_block_stop     {index: 0}
← content_block_start    {index: 1, content_block: {type: "tool_use", id: "toolu_1", name: "bash"}}
← content_block_delta    {index: 1, delta: {type: "input_json_delta", partial_json: "{\"command\":"}}
← content_block_delta    {index: 1, delta: {type: "input_json_delta", partial_json: "\"ls\"}"}}
← content_block_stop     {index: 1}
← message_delta           {delta: {stop_reason: "tool_use"}, usage: {output_tokens: 30}}
← message_stop
  ... (engine 执行工具，发起第二轮 LLM 调用) ...
← message_start          {message_id: "msg_2", request_id: "r1"}
← content_block_start    {index: 0, content_block: {type: "text"}}
← content_block_delta    {index: 0, delta: {type: "text_delta", text: "当前目录有..."}}
← content_block_stop     {index: 0}
← message_delta           {delta: {stop_reason: "end_turn"}, usage: {output_tokens: 50}}
← message_stop
← result                  {request_id: "r1", status: "success", num_turns: 2, duration_ms: 3200}
```

错误：

```
→ user.message           {request_id: "r2", text: "..."}
← error                   {error: {type: "rate_limit_error", code: "rate_limit_exceeded", message: "..."}, request_id: "r2"}
← result                  {request_id: "r2", status: "error", reason: "blocking_limit"}
```
