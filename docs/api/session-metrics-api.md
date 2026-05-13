# Session Metrics API 参考文档

> **更新记录**
>
> | 日期 | 变更 |
> |------|------|
> | 2026-05-13 | 初版：`GET /api/v1/sessions/{id}/metrics` 端点 |
> | 2026-05-13 | 端点挂载点从 WebSocket 端口（`:8081`）迁移到 Console 管理端口（`:8090`），与其他 REST 管理接口统一 |

Session Metrics API 暴露每个 session 的 token / 延迟 / 子 agent / 上下文窗口运行指标，供仪表盘 UI 按需拉取并渲染。

- **Base URL**: `http://localhost:8090`（默认；与 Console API 共用端口和同一 HTTP server）
- **路由前缀**: `/api/v1/sessions/`
- **Content-Type**: `application/json`
- **数据来源**: 优先内存中的实时 Tracker；不存在则回退到 SQLite `sessions.metrics_json` 列
- **鉴权**: 暂无（`session_id` 作为难猜的隐式凭证；与 Console API 同一安全域）

### 与 Console API 的关系

两类接口**部署在同一端口** `:8090`，按路由前缀区分：

| 前缀 | 用途 | 响应包络 |
|------|------|----------|
| `/console/v1/...` | Agent 定义运维（增删改查、导入导出） | `{code: "OK", data: {...}}` |
| `/api/v1/sessions/{id}/metrics` | 仪表盘读取运行指标 | 直接平铺业务数据 |

包络不统一是有意的：仪表盘前端直接消费 `SessionStats` 对象，加一层 `data` 包装是负担；运维场景需要 `total` / `code` 这种元数据，Console API 包络合理。两类前缀互不影响。

**注意**：当 `cfg.Console.Enabled=false` 时整个 `:8090` server 不启动，Session Metrics 端点也随之关闭。

---

## 统一响应格式

### 成功响应

200 OK，响应体是 [SessionStats](#sessionstats-schema) 对象的完整 JSON 序列化。

### 错误响应

```json
{
  "error": "<error_code>",
  "message": "<可选,人类可读说明>"
}
```

### 错误码一览

| error | HTTP Status | 说明 |
|-------|-------------|------|
| `bad_request` | 400 | 路径格式错误（如 `session_id` 缺失或含非法字符 `/`） |
| `session_not_found` | 404 | session 既不在内存 Tracker，也不在 SQLite 中 |
| `method_not_allowed` | 405 | 仅支持 `GET` |
| `internal` | 500 | DB 读取失败 / 序列化失败等服务端错误 |

---

## SessionStats Schema

```json
{
  "session_id":         "string   (session 标识)",
  "updated_at":         "RFC3339  (最近一次内存 Tracker 写入的时间戳, UTC)",

  "input_tokens":       "int64    (累计输入 token, 不含 cache_read)",
  "output_tokens":      "int64    (累计输出 token)",
  "latency_ms_total":   "int64    (累计 LLM 调用 wall-clock 毫秒)",
  "latency_ms_avg":     "int64    (= latency_ms_total / llm_calls)",

  "cache_read_tokens":  "int64    (cache 命中读取 token)",
  "cache_write_tokens": "int64    (cache 写入 token)",
  "cache_hit_rate":     "float64  (= cache_read / (cache_read + input_tokens))",
  "thinking_tokens":    "int64    (extended thinking / reasoning token)",
  "thinking_share":     "float64  (= thinking_tokens / output_tokens)",

  "context_window":     "ContextWindowStats (最近一次 LLM 调用的输入窗口构成)",

  "per_model":          "[ModelStats] (按 model 拆分;客户端用此算 cost)",
  "subagents":          "[SubAgentStats] (子 agent 明细表)",

  "llm_calls":          "int      (累计 LLM 调用次数,含失败尝试)",
  "tool_calls":         "int      (累计工具调用次数)"
}
```

### ContextWindowStats

最近一次 LLM 调用的输入窗口快照（不是累计值,是"as of latest call"）。

```json
{
  "used":          "int64  (LLM 实际接收的输入 token;来自 provider 返回的真实值)",
  "limit":         "int64  (模型最大输入上限,来自 config.MaxTokens)",
  "history":       "int64  (历史 user/assistant text 估算占比)",
  "tool_results":  "int64  (工具结果 content block 估算占比)",
  "system_prompt": "int64  (system prompt + 工具 schema 估算占比)"
}
```

> `history` / `tool_results` / `system_prompt` 是按字符数 / 4 的廉价估算（用于仪表盘看比例分布）；`used` 由 provider 报告的真实 input token 覆盖。

### ModelStats

每个 model 的 token 分项。**客户端遍历此数组,乘以自己维护的定价表,得到 USD 总成本** —— 服务端故意不算钱。

```json
{
  "model":              "string  (如 claude-opus-4-7 / claude-sonnet-4-6)",
  "input_tokens":       "int64",
  "output_tokens":      "int64",
  "cache_read_tokens":  "int64",
  "cache_write_tokens": "int64",
  "thinking_tokens":    "int64",
  "llm_calls":          "int"
}
```

### SubAgentStats

子 agent 表格中的一行。`agent_run_id` 作为主键,同一 agent 类型多次运行得到独立行。

```json
{
  "agent_run_id":       "string  (子 agent 运行实例 id)",
  "agent_id":           "string  (子 agent 标识)",
  "agent_type":         "string  (如 researcher / planner)",
  "model":              "string  (该 agent 主用 model;跨多个 model 时标 \"mixed\")",

  "input_tokens":       "int64",
  "output_tokens":      "int64",
  "cache_read_tokens":  "int64",
  "cache_write_tokens": "int64",
  "thinking_tokens":    "int64",
  "total_tokens":       "int64   (= input + output,不含 cache;占比条排序键)",

  "llm_calls":          "int",
  "duration_ms":        "int64   (从 subagent_start 到 subagent_end 的 wall-clock)",
  "status":             "string  (running | completed | failed | model_error | aborted)"
}
```

---

## 端点

### 1. 查询 Session 指标

返回指定 session 的当前指标快照。

**请求**

```
GET /api/v1/sessions/{session_id}/metrics
```

**路径参数**

| 参数 | 类型 | 说明 |
|------|------|------|
| `session_id` | string | 待查询的 session 标识。必须非空、不能包含 `/` |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "session_id": "sess_e2e",
  "updated_at": "2026-05-13T10:00:00Z",
  "input_tokens": 12450,
  "output_tokens": 3210,
  "latency_ms_total": 18200,
  "latency_ms_avg": 1820,
  "cache_read_tokens": 8900,
  "cache_write_tokens": 450,
  "cache_hit_rate": 0.4173,
  "thinking_tokens": 680,
  "thinking_share": 0.2118,
  "context_window": {
    "used": 12450,
    "limit": 200000,
    "history": 8200,
    "tool_results": 3800,
    "system_prompt": 450
  },
  "per_model": [
    {
      "model": "claude-opus-4-7",
      "input_tokens": 9800,
      "output_tokens": 2800,
      "cache_read_tokens": 6500,
      "cache_write_tokens": 300,
      "thinking_tokens": 680,
      "llm_calls": 7
    },
    {
      "model": "claude-sonnet-4-6",
      "input_tokens": 2650,
      "output_tokens": 410,
      "cache_read_tokens": 2400,
      "cache_write_tokens": 150,
      "thinking_tokens": 0,
      "llm_calls": 3
    }
  ],
  "subagents": [
    {
      "agent_run_id": "run_e5",
      "agent_id": "sub_e5",
      "agent_type": "researcher",
      "model": "claude-sonnet-4-6",
      "input_tokens": 1820,
      "output_tokens": 210,
      "cache_read_tokens": 3400,
      "cache_write_tokens": 100,
      "thinking_tokens": 0,
      "total_tokens": 2030,
      "llm_calls": 3,
      "duration_ms": 4200,
      "status": "completed"
    }
  ],
  "llm_calls": 10,
  "tool_calls": 14
}
```

**错误响应**

```
HTTP/1.1 404 Not Found

{"error": "session_not_found"}
```

```
HTTP/1.1 400 Bad Request

{"error": "bad_request", "message": "expected /api/v1/sessions/{id}/metrics"}
```

```
HTTP/1.1 405 Method Not Allowed

{"error": "method_not_allowed", "message": "only GET is supported"}
```

```
HTTP/1.1 500 Internal Server Error

{"error": "internal"}
```

**curl 示例**

```bash
curl -s http://localhost:8090/api/v1/sessions/sess_abc/metrics | jq .
```

---

## 数据来源与一致性

### 双层数据源

```
┌────────────────────────────────────────────────────────┐
│  优先: 内存 Tracker (sessionstats.Registry)              │
│    └─ 实时反映正在进行的 session                          │
└────────────────────────────────────────────────────────┘
                          │ miss
                          ▼
┌────────────────────────────────────────────────────────┐
│  回退: SQLite sessions.metrics_json 列                   │
│    └─ session 已 archive / 服务重启后冷加载              │
└────────────────────────────────────────────────────────┘
```

handler 先查内存 Tracker（`sessionstats.Registry.Get`）：

- **命中**：返回 `Tracker.Snapshot()` 的深拷贝。session 正在跑或刚跑完未到 idle archive 阈值时走这条路径，数据最新。
- **未命中**：查 SQLite。session 已被 `Manager.CleanupIdle` 清出内存、或服务重启后尚未被任何 GetOrCreate 重新加载时走这条路径。返回的是上次落盘的快照。

### 落盘节奏

`statsPersistWorker` 每个 session 一个,通过 dirty 信号 + 1 秒 debounce 把 Tracker 快照写入 SQLite:

- `Tracker.RecordLLMCall` / `RecordToolCall` / `Start/FinishSubAgent` 都会 fire 一次 notify
- worker 收到信号后等 1 秒,期间所有进一步的 notify 都被合并
- `trace_finished` / `trace_failed` 事件触发 `Manager.FlushStats` 强制立即落盘,保证 trace 终止后 HTTP 立刻能取到最终值
- `Manager.CleanupIdle` 在 archive session 之前也强制落盘

### 服务重启行为

`session.Manager.GetOrCreate` 在创建或重新加载 session 时:

1. 通过 `Registry.GetOrCreate` 取得 Tracker
2. 调用 `Store.LoadSessionStats` 取回上次落盘的快照
3. 调用 `Tracker.RestoreFrom(snap)` 把数据填回内存（仅当 Tracker 为空时生效,避免覆盖正在跑的统计）
4. 启动 `statsPersistWorker` 开始监听 dirty 信号

因此重启不丢累计数据。

---

## 客户端使用指南

### 计算 USD 成本

服务端**故意**不返回任何 USD 字段(定价表会变、企业折扣不一致、客户端常需自定义)。客户端按以下流程计算:

```typescript
// 客户端维护的定价表（示例,实际价格以厂商最新为准）
const pricing: Record<string, {
  input: number;       // USD per 1M input tokens
  output: number;
  cache_read: number;  // 通常是 input 的 10%
  cache_write: number; // 通常是 input 的 125%
  thinking: number;    // 通常等于 output
}> = {
  "claude-opus-4-7":     { input: 15.0, output: 75.0, cache_read: 1.5,  cache_write: 18.75, thinking: 75.0  },
  "claude-sonnet-4-6":   { input: 3.0,  output: 15.0, cache_read: 0.3,  cache_write: 3.75,  thinking: 15.0  },
  "claude-haiku-4-5":    { input: 1.0,  output: 5.0,  cache_read: 0.1,  cache_write: 1.25,  thinking: 5.0   },
};

function computeCost(stats: SessionStats): number {
  let total = 0;
  for (const m of stats.per_model) {
    const p = pricing[m.model];
    if (!p) continue;  // 未知 model,跳过或按默认价
    total += (m.input_tokens       * p.input
           +  m.output_tokens      * p.output
           +  m.cache_read_tokens  * p.cache_read
           +  m.cache_write_tokens * p.cache_write
           +  m.thinking_tokens    * p.thinking) / 1_000_000;
  }
  return total;
}
```

### Cache 节省的展示

```typescript
function cacheSavedUSD(stats: SessionStats): number {
  // 假设这些 cache_read token 全部按 input 价收 -> 实际按 cache_read 价收 = 节省额
  let saved = 0;
  for (const m of stats.per_model) {
    const p = pricing[m.model];
    if (!p) continue;
    saved += m.cache_read_tokens * (p.input - p.cache_read) / 1_000_000;
  }
  return saved;
}
```

### 子 agent 表格排序

按 `total_tokens` 降序得到 "token 大户" 排序（不含 cache,因为 cache 命中不该被算"消耗大"）:

```typescript
const sortedRows = [...stats.subagents].sort((a, b) => b.total_tokens - a.total_tokens);
```

### 轮询频率

- 仪表盘**不需要实时推送** —— 没有 WebSocket 频道推 stats
- 客户端按需 `GET`,常见间隔 2~5 秒
- 服务端 Tracker 写入有 1 秒 debounce,因此即便客户端轮询更密也不会得到更细粒度的数据

---

## 字段语义注意点

- **`input_tokens` 不含 `cache_read_tokens`**：两者是平行计数,需要"实际计费输入总量"时取 `input_tokens + cache_read_tokens + cache_write_tokens`。
- **`output_tokens` 是否含 `thinking_tokens` 取决于上游 Provider**：Anthropic 分开报、OpenAI 把 reasoning 算进 completion_tokens。服务端按 Bifrost SDK 透传,不做归一化。客户端展示"思考占比"时直接用 `thinking_share` 即可。
- **`total_tokens` 仅在 `subagents[]` 里出现**：等于 `input_tokens + output_tokens`(不含 cache 和 thinking 重复计),用于表格占比条排序。顶层不复述这个字段。
- **`updated_at` 是内存 Tracker 的写入戳**：不代表 SQLite 落盘时间。需要"上次落盘时间"用 SQLite 表的 `updated_at` 列（session 行级别）。
- **`context_window.used` 只反映最近一次 LLM 调用**：不是累计、不是平均。用于上下文窗口面板的"为什么这么满"诊断。

---

## 已知限制

- **404 边界含糊**: 当 session 行存在于 SQLite 但 `metrics_json` 列为空时（极端情况：session 刚创建、第一次 metrics flush 之前 worker 失败、且内存 Tracker 已被 idle 清出），handler 返回 404 而非 200+ 空对象。实际触发概率极低。
- **`bindStatsLocked` 持 Manager 写锁时访问 SQLite**: 高并发 GetOrCreate 场景下,慢 SQLite 读会阻塞其他 session 的 GetOrCreate / Get。当前默认 SQLite busy_timeout=5s 是上界。
- **无 list 端点**: 本期仅支持按 `session_id` 单点查。需要"所有 session 的指标总览"时,基础设施已存在(`storage.Storage.ListSessions`),但未对外暴露。

---

## 相关文档

- 设计 spec: `docs/superpowers/specs/2026-05-12-session-metrics-design.md`（仅本地,被 gitignore）
- WebSocket 协议: `docs/protocols/websocket.md`
- Console API: `docs/api/console-api.md`
