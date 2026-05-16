# WebSocket Protocol v2 — UI-First Card Model

- **Version**: 0.5.0 (绑定 emit v2.2 实现，参见 `docs/emit/2026-05-07-protocol-v2.2-card.md`)
- **状态**: 实施候选；内测阶段——**唯一对外协议**，单一 endpoint `/v1/ws`
- **核心定位**: 这是驱动 AI 助手客户端 UI 渲染与状态展示的协议（不是分布式追踪/观测协议）

---

## Changelog

只记录**与客户端协议相关**的变更（wire shape、新能力位、URL、客户端必须感知的行为）。
纯服务端内部改动（bug 修复、调优、配置默认值、内部重构）不进此表——参见 git 提交记录。

版本号 `MAJOR.MINOR.PATCH`：

- **MAJOR** — wire 协议骨架变化（动作集合 / 顶层 Event 结构改变）
- **MINOR** — 新能力（新事件 kind / 新可选字段 / 新 capability 位）
- **PATCH** — 文字修订；客户端无需任何变更

| 版本 | 日期 | 变更 |
|---|---|---|
| **0.6.0** | 2026-05-16 | **新增 `card_kind=system`（向前兼容增量）**：框架级系统提示通用通道（配置缺失、能力降级、密钥过期等场景共用），不再为每条系统通知单独开 card_kind。首个使用场景：`web_search` + `tavily_search` 全部 disabled 时，受影响 TierSubAgent 首次 spawn 弹一张 `card.add(kind=system)`，title=`"搜索能力不可用"`、`hint.icon=warning`、`payload.action_hint` 给出 yaml 配置启用步骤。session 级去重（同 session 不重复弹）。老客户端按 `LookupCardMeta` 默认 fallback 渲染为通用 system 卡，不崩。详见 §5 表 + §10.9。 |
| **0.5.0** | 2026-05-09 | **失败决定门 + 拓扑/Watchdog 调整（向前兼容增量）**：1) 新增 `prompt.user(kind=step_decision)` —— Scheduler / PlanCoordinator 在 step 重试用尽 / re-plans 用尽 / planner 错误时不再静默 fallback，而是弹给用户决定 `continue` / `retry` / `cancel`，详见 §7.1 kind=step_decision；2) `prompt.user_response.decision` 增加合法取值 `continue` / `retry` / `cancel`（仅对 `step_decision` 生效，其它 kind 仍用 `approved` / `denied`），详见 §7.3；3) plan / orchestrate 派出来的 `agent` 卡现在 `parent_card_id` 指向对应 `step` 卡（之前指 tool / message / turn）—— 按 `parent_card_id` 自动布局的客户端无需改动；硬编码 step 与 agent 同级的需要支持嵌套；4) `prompt.user` 期间所有祖先 tracked 卡片的 orphan watchdog 暂停，用户思考再久也不会出现 `orphan_timeout` 关闭——客户端无变化，纯改善。 |
| **0.4.0** | 2026-05-08 | **plan 审阅与 question 行为对齐**：`prompt.user(kind=plan_review)` 阶段服务端**不**开 plan card、不启 watchdog、等待无上限——与 `kind=question` 完全同构。`card.add(plan)` 只在用户 approve 之后才发出。客户端必须基于 `prompt.user.payload.inner.steps` 直接渲染审阅视图，**不要**等 `card.add(plan)`。详见 §7.1 kind=plan_review 渲染规则。 |
| **0.3.0** | 2026-05-07 | **故障恢复（新 capability）**：服务端重启后未答 prompt（permission/question/plan_review）会按同 `request_id` 自动重发到重连客户端；客户端必须按 `request_id` 去重 UI（同一 ID 不弹两次模态）。新增 `session.event(opened).capabilities.recovery` 能力位。详见 §2.4.2 / §2.4.4。 |
| **0.2.0** | 2026-05-07 | **AskUserQuestion 升格为一等 prompt**：原走 `tool.call`/`tool.result` 的 client-routed 路径，现 wire 上以 `prompt.user(kind=question)` + `prompt.user_response` 表达。客户端 UI 不再用工具卡片渲染，改用问答模态。 |
| **0.1.1** | 2026-05-07 | **endpoint 收敛**：移除 `/v2/ws` 多版本并存设想，**唯一 endpoint = `/v1/ws`**（"v1" 是产品版本号，不是协议代号）。客户端连接 URL 同步调整。 |
| **0.1.0** | 2026-05-07 | **初始 v2.2 协议规范**：UI-first 卡片模型；8 个动作（`card.add/set/append/tick/close` + `prompt.user/reply` + `session.event`） × 12 个 card_kind 矩阵；统一 `ErrorInfo`；`artifact://` URI 协议级硬约束；`Hint` 服务端兜底；session.event(opened) 能力声明。**与历史 v1 47-event 协议不兼容**。 |

---

## 1. Overview

### 1.1 设计目标

| # | 原则 | 含义 |
|---|---|---|
| 1 | **UI-first 命名** | 协议命名 = 前端拿到事件后该做什么 |
| 2 | **卡片为一等公民** | 客户端把所有渲染对象抽象为 `Card`，协议直接服务这个抽象 |
| 3 | **框架触发，LLM 喂料** | 所有事件由 Go 代码（emit Builder）发出；LLM 仅通过受控通道提供文本素材 |

### 1.2 单一协议 / 单一 endpoint

内测期间不维护多版本并存。本协议是当前**唯一**对外暴露的 WebSocket 协议，挂在 `/v1/ws`（"v1" 是产品版本号，不是协议代号）。原 47 个事件类型完全合并为 8 个动作 × 13 个 card_kind 的矩阵：

| v1 | v2 |
|---|---|
| `trace.* / task.end / plan.* / step.* / subagent.* / agent.* / message.* / content.* / tool.* / task.* / team.* / deliverable.*` | `card.add / card.set / card.append / card.tick / card.close` |
| `permission.request` | `prompt.user(kind=permission)` |
| `agent.intent`（顶层 + subagent.event 包装） | `card.tick(kind=intent)` 或 `card.add(tool).payload.intent` |
| 5 套错误结构（ErrorDetail/ErrorBody/FailurePayload/...） | 1 套 `ErrorInfo` |
| `session.created/updated/resumed/resume_failed` | `session.event(kind=...)`（`pong` 例外，详见 §2.3） |

不保留向前兼容；v2 走独立 endpoint（`/v1/ws`）。

### 1.3 容量声明

握手时通过 `session.event(kind=opened).payload.capabilities` 声明：

| Capability | 含义 |
|---|---|
| `streaming`     | 支持 LLM 流式（card.append channel=text） |
| `tools`         | 支持服务端工具执行（card add+close target=server） |
| `client_tools`  | 支持客户端工具执行（card add+close target=client） |
| `sub_agents`    | 支持 sub-agent 嵌套（card_kind=agent） |
| `tasks`         | 支持 TodoList（card_kind=todo） |
| `teams`         | 支持团队（card_kind=team） |
| `plan_review`   | 支持 plan 审阅（prompt.user kind=plan_review） |
| `artifacts`     | 支持工件产出（card_kind=artifact + artifact:// URI 引用） |

---

## 2. Connection

### 2.1 Endpoint

```
WSS scheme:  wss://host:port/v1/ws
WS  scheme:  ws://host:port/v1/ws
```

**默认端口**：8081（`channels.websocket.port`，可通过 config 覆盖）

### 2.2 Handshake

1. 客户端发起 WebSocket 升级
2. 服务端回 HTTP 101 Switching Protocols
3. 服务端立刻推 `session.event(kind=opened)` 帧
4. 客户端发 `user.message`（或更早的 `session.create` 配置帧）

### 2.3 Keep-Alive

客户端每 30s 发 `{"type":"ping"}`；服务端回 `{"type":"pong"}`。两者都是**极简顶层帧**——不带 `envelope`、不占 `seq`、不携带 `severity` / `agent_id` 等业务字段，纯链路连通性信号；与所有 card / prompt / session.event 事件解耦。30s 内无心跳则任何一端可断开。

```jsonc
// client → server
{"type": "ping"}

// server → client
{"type": "pong"}
```

### 2.4 Reconnect & 故障恢复

#### 2.4.1 事件流续传（in-flight turn）

客户端收到 `session.event(kind=resumed/resume_failed)` 决定走续传或全量刷新。续传：

```json
→ {"type":"session.resume","trace_id":"tr_xxx","last_seq":42}
← {"type":"session.event","payload":{"kind":"resumed","inner":{
     "trace_id":"tr_xxx","from_seq":43,"to_seq":68
   }}}
... 服务端按 seq 顺序补发事件 ...
```

#### 2.4.2 未答 Prompt 自动恢复（核心特性）⭐

服务端关闭/重启时，**未回答的 prompt（permission / question / plan_review）会被持久化**。客户端用同 `session_id` 重连后：

1. **服务端在 `session.event(kind=opened)` 之后立即重发所有未答 prompt**——按当时 emit 的同一 `request_id` 字节级原样重发
2. 客户端把"重发的 prompt"和"原本看到的 prompt"按 `request_id` **去重**——同一个 `request_id` 视为同一个 prompt，UI 不应弹两个模态
3. 客户端正常用 `prompt.user_response` 回答；服务端重新进入引擎 query loop，把答案合成为续接 user.message 注入对话

**TTL**：未答 prompt 默认 **15 天**后自动清理（abandoned conversations 不会无限累积）。服务端每小时跑一次清理。

**典型时序**：

```
[t=0]  服务端 emit prompt.user{request_id:"req_abc", kind:"question"}
       (同时持久化到 SQLite pending_waits 表)
[t=1]  服务端 crash / restart
[t=2]  客户端用相同 session_id 重连
       → session.create
[t=3]  服务端 ← session.event(kind=opened) {capabilities:{recovery:true}}
[t=4]  服务端 ← prompt.user{request_id:"req_abc", ...} (重发)
[t=5]  客户端检测 request_id 已经存在于 pending UI 列表
       → 不弹新模态 (or 弹一个但确保只有一个 visible)
[t=6]  用户在原有 UI 上回答 → prompt.user_response{request_id:"req_abc", ...}
[t=7]  服务端 conn.handlePromptResponse:
       - in-memory miss (新进程 translator map 是空的)
       - SQLite hit (req_abc 还在表里)
       - 调 Resumer.Resume(wait, answer)
       - Resumer 把答案合成成续接 user.message 透传给引擎
[t=8]  引擎 LLM 看到上下文 + 用户答案，自然继续推理
[t=9]  服务端从 SQLite 删除 req_abc (防止重放)
```

**幂等保证**：用户重复点击"答案"按钮，服务端只会接受第一次（命中即删）；后续重复回答返回 `session.event(kind=error)`。

#### 2.4.3 capabilities.recovery

`session.event(kind=opened)` 的 `capabilities.recovery` 字段标识服务端是否启用恢复：

- `true` — 服务端会持久化未答 prompt 并在重连时重发
- `false` 或缺失 — 服务端无恢复能力，重启会丢失未答 prompt（客户端遇到 wire `error` "unknown request_id" 时应展示 "对话已中断，请重新提问"）

#### 2.4.4 客户端实现 checklist

| # | 客户端必须做的事 |
|---|---|
| 1 | **使用稳定 session_id 重连**——浏览器刷新 / 网络掉线 / app 重启都用同一个 ID（用户视角的"当前对话"）|
| 2 | **`session.event(kind=opened)` 后准备接 prompt 重发**——下一帧可能是 prompt.user，不要立即假定握手已完毕 |
| 3 | **按 request_id 对 prompt 去重**——重发的 prompt 与原 prompt 有相同 `request_id`，UI 应共用同一个模态/卡片 |
| 4 | **回答只发一次**——服务端 forget 后再发会被拒绝；UI 收到 error 后应隐藏模态 |
| 5 | **检查 capabilities.recovery**——为 false 时禁用"先离开等会再答"的引导文案，告知用户必须立即回答 |
| 6 | **TTL 感知**——超过 15 天未答的 prompt 服务端会清；UI 应在显示时间过久（如 >7 天）的 prompt 时给出"已过期可能"提示 |

---

## 3. Message Format

### 3.1 顶层结构

每条服务端→客户端事件都是这个壳子（JSON）：

```json
{
  "type":     "card.add | card.set | card.append | card.tick | card.close | prompt.user | prompt.reply | session.event",
  "envelope": { ... },           // 必填
  "hint":     { ... },           // UI 渲染提示，服务端兜底
  "metrics":  { ... },           // 可选，仅 card.close 携带
  "payload":  { ... }            // 由 type / card_kind 决定 schema
}
```

### 3.2 Envelope（必填）

```json
{
  "event_id":       "evt_<20hex>",
  "session_id":     "sess_<id>",
  "trace_id":       "tr_<24hex>",
  "card_id":        "<id>",                  // card.* 事件必填，prompt./session. 留空
  "parent_card_id": "<id>",                  // 可选
  "card_kind":      "turn|message|tool|agent|plan|step|artifact|thinking|memory_op|budget|todo|team|system",
  "seq":            42,                      // 全 trace 单调递增，从 1 开始
  "timestamp":      "2026-05-07T10:00:00.000Z",
  "agent_id":       "main|sub_e5|...",       // 该事件归属哪个 agent
  "agent_role":     "persona|orchestrator|worker|system",
  "agent_run_id":   "run_<16hex>",
  "severity":       "info|warn|error"
}
```

**核心契约**：所有 sub-agent context 内的事件必须填 `agent_id` 等于 `card.add(agent)` 时的 ID。framework 通过 emit Builder 的 `Sub()` 方法强制这一点。

### 3.3 Hint（可选但服务端总是填）

UI 渲染建议，**不是协议契约**——客户端可覆盖。

```json
{
  "title":         "Bash",                   // 必填，注册表模板派生
  "summary":       "列目录...",
  "icon":          "tool",                   // 默认按 card_kind 派生
  "initial_state": "expanded|collapsed|hidden",
  "persona":       "..."                     // 给 L1 的发声口吻
}
```

### 3.4 Metrics（仅 card.close）

```json
{
  "duration_ms":         50,
  "tokens_in":           120,
  "tokens_out":          80,
  "cache_read_tokens":   10000,
  "cache_write_tokens":  500,
  "cost_usd":            0.0023,
  "model":               "claude-opus-4-7",
  "budget_spent": {"tokens": 12000, "usd": 0.05},
  "budget_limit": {"tokens": 50000, "usd": 1.00}
}
```

---

## 4. 事件动作（8 个）

| Type | 用途（前端动作） | 类别 |
|---|---|---|
| `card.add`      | 新建一张卡 | State |
| `card.set`      | 卡片字段覆盖 | State |
| `card.append`   | 卡片内流式追加（按 channel 区分） | Stream |
| `card.tick`     | 卡片内瞬时信号（progress/heartbeat/intent/note/escalation），可丢可节流 | Telemetry |
| `card.close`    | 卡片终态（status: ok/failed/skipped/cancelled） | State |
| `prompt.user`   | 系统问用户（permission/question/plan_review）；阻塞 UI | Interaction |
| `prompt.reply`  | 服务端 echo 用户响应（成功/拒绝/超时） | Interaction |
| `session.event` | 会话级事件（payload.kind 区分 opened/updated/error/resumed/resume_failed） | Lifecycle |
| `ping` / `pong` | 链路连通性帧（无 envelope；详见 §2.3） | Keep-Alive |

---

## 5. Card Kinds（13 种）

| CardKind | 描述 | 父类型典型值 | 默认渲染建议 |
|---|---|---|---|
| `turn`     | 一轮请求 | (根) | timeline 容器 |
| `message`  | 一条 LLM 回复 | turn | 聊天气泡 |
| `tool`     | 一次工具调用 | message / agent | 折叠工具卡 |
| `agent`    | sub-agent 会话 | tool / agent | 角色面板 |
| `plan`     | 任务图 | turn / agent | 折叠表格 |
| `step`     | plan 中一步 | plan | 表格内一行 |
| `artifact` | 产出物 | tool / agent | 右侧附件 chip |
| `thinking` | extended thinking | turn / agent | 思考折叠区 |
| `memory_op`| memory 读写/压缩 | turn / agent | 系统通知条 |
| `budget`   | 预算告警 | turn / plan | 横幅警告 |
| `todo`     | TodoList 项 | turn / agent | TodoList 控件项 |
| `team`     | 团队组 | turn | 团队面板 |
| `system`   | 框架级系统提示（配置/能力缺失等） | (无 / 根) | 顶部通知条；`hint.icon=warning` 时按警告样式 |

---

## 6. Card Action 详解

### 6.1 `card.add` — 新建卡

```json
{
  "type": "card.add",
  "envelope": {
    "card_id": "tool_x", "card_kind": "tool",
    "parent_card_id": "msg_1", ...
  },
  "hint": {"title": "Bash", "icon": "tool"},
  "payload": {
    "name": "bash",
    "target": "server",
    "intent": "列目录",
    "input": {"command": "ls -la"}
  }
}
```

**Payload schema by card_kind**：见 §10。

### 6.2 `card.set` — 字段覆盖

```json
{
  "type": "card.set",
  "envelope": {"card_id": "step_a", "card_kind": "step", ...},
  "payload": {"status": "running"}      // 部分字段，前端按字段名覆盖
}
```

### 6.3 `card.append` — 流式追加

```json
{
  "type": "card.append",
  "envelope": {"card_id": "msg_1", "card_kind": "message", ...},
  "payload": {
    "channel": "text|tool_input|thinking",
    "index":   0,                       // 多 block 时区分
    "chunk":   "Hello world",           // for channel=text|thinking
    "partial_json": "{\"command\":"     // for channel=tool_input
  }
}
```

**约束**：`card.append` **不可丢**，必须按顺序送达；客户端按 `(card_id, channel, index)` 累积缓冲。

### 6.4 `card.tick` — 瞬时信号

```json
{
  "type": "card.tick",
  "envelope": {"card_id": "tool_x", "card_kind": "tool", ...},
  "payload": {
    "kind": "progress|heartbeat|intent|note|escalation",
    "inner": { ... }
  }
}
```

**节流规则**：
- `progress` / `heartbeat` 必须节流（建议 200-500ms），**可丢弃**
- `intent` / `note` / `escalation` **不可丢**

inner schema 详见 §11。

### 6.5 `card.close` — 终态

```json
{
  "type": "card.close",
  "envelope": {"card_id": "tool_x", "card_kind": "tool", ...},
  "metrics": {"duration_ms": 50, ...},
  "payload": {
    "status": "ok|failed|skipped|cancelled",
    "error":  { ... },                   // 仅 status=failed
    "inner":  { ... }                    // type-specific 终态字段
  }
}
```

---

## 7. Prompt Actions

### 7.1 `prompt.user` — 服务端问用户

```json
{
  "type": "prompt.user",
  "envelope": {...},
  "payload": {
    "request_id": "req_<16hex>",
    "kind":       "permission|question|plan_review|step_decision",
    "inner":      { ... },
    "timeout_ms": 60000                  // 0 = 无超时
  }
}
```

**inner schema by kind**：

#### kind=permission
```json
{
  "tool_name":     "Bash",
  "tool_input":    "rm -rf /tmp",
  "message":       "Allow shell?",
  "is_read_only":  false,
  "options": [
    {"label": "Allow once",    "scope": "once",    "allow": true},
    {"label": "Allow session", "scope": "session", "allow": true},
    {"label": "Deny",          "scope": "once",    "allow": false}
  ],
  "permission_key": "Bash:rm"
}
```

#### kind=question
```json
{
  "question": "你希望覆盖中文论文吗？",
  "options": [
    {"label": "是"}, {"label": "否"}, {"label": "都看"}
  ],
  "multi":        false,
  "allow_custom": true
}
```

#### kind=plan_review
```json
{
  "plan_id":   "pln_xxx",
  "goal":      "调研 X 写 Y",
  "rationale": "research+write 模式",
  "steps": [
    {"id": "s1", "subagent_type": "researcher", "description": "调研"},
    {"id": "s2", "subagent_type": "writer",     "description": "撰写", "depends_on": ["s1"]}
  ],
  "available_subagents": ["researcher", "writer"],
  "rejection_reason":    "<上次拒绝原因，可选>"
}
```

**渲染规则（与 `kind=question` 同构）**：

`prompt.user(plan_review)` 是**等用户审阅**的事件，本身不开任何 card；**不要**依赖 `card.add(plan)` 才渲染 plan 树。客户端必须基于 `payload.inner.steps`（含 `id` / `description` / `prompt` / `depends_on` / `subagent_type`）直接绘制审阅视图，并展示 approve / reject / edit 三种入口。

两阶段对应两种 UI：

| 阶段 | 服务端行为 | 客户端 UI |
|---|---|---|
| **审阅中**（`prompt.user(plan_review)` 已发） | 无 plan card，无 watchdog，等待时长无上限 | 渲染待审阅模态（基于 prompt payload 的 steps） |
| **执行中**（用户 approve 后） | `card.add(plan)` 启动；同时 `card.add(step)` 流入 | 切换为执行视图（基于 plan card payload + step 子卡） |

注意事项：
- 审阅期间用户思考 30 分钟、1 小时都不会触发任何超时——和 `AskUserQuestion`(kind=question) 行为完全一致
- 服务端重启会重发同一 `request_id` 的 `prompt.user(plan_review)`，客户端按 `request_id` 去重模态（不要弹两次）
- 用户 approve 之后才会看到 `card.add(plan)`；`card.add(plan)` 出现意味着已进入执行阶段，不要再展示审阅 UI

#### kind=step_decision
```json
{
  "scope":            "step",                          // "step" | "plan"
  "step_id":          "s2",                            // scope=step 时填，scope=plan 时为空
  "step_description": "调研 Agent OS 最近半年开源进展",
  "reason":           "rate limit exceeded (+2 more)", // 服务端拼好的失败摘要，原样展示
  "attempts":         3,                               // 已尝试次数（包含触发失败的那一次）
  "allow_retry":      true                             // false 时客户端禁用"重试"按钮
}
```

**何时触发**：

服务端在以下情况会暂停 plan 并发出 `prompt.user(kind=step_decision)`，把"是否继续/重试/取消"的选择权交给用户——而不是默默 fallback 让用户对着已经花掉的 token 一头雾水：

| 触发点 | scope | allow_retry | 触发条件 |
|---|---|---|---|
| Scheduler 单 step 失败 | `step` | `true` | step 经过 `MaxStepAttempts`（默认 3）次仍失败（非瞬时类失败也算） |
| PlanCoordinator | `plan` | `false` | re-plans 用尽（`MaxPlanReplans` 默认 3）/ planner 报错 / budget 超线 |

**渲染规则（与 `kind=plan_review` / `kind=question` 同构）**：

`prompt.user(step_decision)` 同样不开任何 card、不启 watchdog、等待无上限。客户端：

- 弹一个三按钮（或两按钮）的决定模态：**继续 / 重试 / 取消**；`allow_retry=false` 时不渲染"重试"
- 模态正文用 `reason` + 上下文（`step_description` / `attempts`）说明出了什么问题，不要自己造文案
- 用户思考再久也不会触发任何超时——服务端在 `prompt.user` 期间会暂停所有祖先卡片的 watchdog，看不到 `card.close{error.type:orphan_timeout}`
- 服务端重启会同 `request_id` 重发；客户端按 `request_id` 去重模态

文案建议（仅 UI 层，不是协议契约）：
- `scope=step`：`步骤 s2「调研 Agent OS …」失败：rate limit exceeded（已重试 3 次）。继续 / 重试 / 取消？`
- `scope=plan`：`计划反复未达成目标：duration budget exhausted。继续（接受当前结果）/ 取消？`

### 7.2 `prompt.reply` — 服务端 echo 决议

```json
{
  "type": "prompt.reply",
  "envelope": {...},
  "payload": {
    "request_id": "req_xxx",
    "decision":   "approved|denied|timeout|cancelled",
    "reason":     "<可选>"
  }
}
```

### 7.3 客户端→服务端：`prompt.user_response`

```json
{
  "type": "prompt.user_response",
  "request_id": "req_xxx",
  "decision":   "approved|denied|continue|retry|cancel",
  "payload": { ... }                     // kind 决定 schema
}
```

`decision` 取值因 `kind` 而异：

| 上行 kind | 合法 `decision` |
|---|---|
| `permission` / `question` / `plan_review` | `approved` / `denied`（保持向前兼容） |
| `step_decision` | `continue` / `retry` / `cancel` |

**kind=permission response**：
```json
{"approved": true, "scope": "once", "message": ""}
```

**kind=question response**：
```json
{"selected_options": ["是"], "custom_text": ""}
```

**kind=plan_review response**：
```json
{
  "approved": true,
  "updated_steps": [ ... ],              // 可选；空数组=按原 plan 执行
  "reason": ""
}
```

**kind=step_decision response**：
```json
{
  "note": "<可选；用户备注，会进 fallback summary>"
}
```

`decision` 走 envelope 顶层（`continue` / `retry` / `cancel`），payload 只携可选 `note`。服务端见到 `decision` 不在合法集时按 `cancel` 处理，避免脏值悬挂等待。

---

## 8. Session Events

```json
{
  "type": "session.event",
  "envelope": {...},
  "payload": {
    "kind":  "opened|updated|error|resumed|resume_failed",
    "inner": { ... }
  }
}
```

#### kind=opened
```json
{
  "protocol_version": "2.0",
  "model":            "claude-opus-4-7",
  "capabilities":     {"streaming": true, "tools": true, "sub_agents": true, ...}
}
```

#### kind=resumed
```json
{"trace_id": "tr_xxx", "from_seq": 43, "to_seq": 68}
```

#### kind=resume_failed
```json
{"trace_id": "tr_xxx", "reason": "events_expired|unknown_trace|session_not_found|not_implemented"}
```

#### kind=error
```json
{
  "error": {
    "type":         "<ErrorType>",
    "message":      "<dev-facing>",
    "user_message": "<persona-friendly>",
    "retryable":    true
  }
}
```

---

## 9. Client → Server Events

| Type | 用途 | 字段 |
|---|---|---|
| `session.create`        | 初始化会话 | `session_id`, `capabilities` |
| `user.message`          | 发送用户消息 | `content` (text/image/file), `coordinator_mode?`, `plan_confirmation?` |
| `user.inject`           | (P3 预留) 实时注入上下文 | `trace_id`, `text` |
| `tool.result`           | 客户端工具回执 | `tool_use_id`, `status`, `output`, `error?`, `metadata?` |
| `prompt.user_response`  | 用户对 prompt.user 的回复 | `request_id`, `decision`, `payload?` |
| `session.interrupt`     | 中断当前 turn | `trace_id` |
| `session.resume`        | 重连续传 | `trace_id`, `last_seq`, `message_cursors?` |
| `ping`                  | 心跳 | (无) |

### 9.1 `user.message` 示例

```json
{
  "type": "user.message",
  "event_id": "c1",
  "content": [
    {"type": "text",  "text": "调研 X 写 Y"},
    {"type": "image", "source": {"type": "path", "path": "/tmp/x.png"}}
  ],
  "coordinator_mode":   "react|plan",
  "plan_confirmation":  "auto|required"
}
```

---

## 10. Card Payload Schemas（按 card_kind）

### 10.1 turn
```json
{"turn_no": 1, "channel": "chat|voice|api"}
```

### 10.2 message
```json
{
  "role": "assistant|user",
  "model": "claude-opus-4-7",
  "stop_reason": "end_turn|tool_use|max_tokens|error"
}
```

### 10.3 tool
```json
{
  "name":        "Bash",
  "target":      "server|client",
  "intent":      "列目录",
  "input":       {"command": "ls -la"},
  "output":      "...",                 // 仅 card.close
  "render_hint": "terminal|code|diff|search|file_info|...",
  "language":    "go",
  "file_path":   "/tmp/x.go",
  "artifacts":   [ ArtifactRef ]
}
```

### 10.4 agent
```json
{
  "name":             "researcher",
  "agent_type":       "sync|async",
  "parent_agent_id":  "main",
  "task_prompt":      "<父 agent 派发的完整 prompt>",
  "output_summary":   "...",            // 仅 card.close
  "num_turns":        3,
  "denied_tools":     ["WebFetch"],
  "artifacts":        [ ArtifactRef ]
}
```

**父卡（envelope.parent_card_id）拓扑**：

| 派出场景 | 父卡 |
|---|---|
| plan / orchestrate 派出（v0.5.0+） | 对应的 `step` 卡 |
| Specialists / Agent 工具直接派出 | 调用方的 `tool` 卡 |
| 嵌套子 agent | 父 agent 的 `agent` 卡 |
| 兜底 | `message` / `turn` |

plan 模式下渲染层级因此是 `turn → message → plan → step → agent → tool/...`。这条变化纯靠 `parent_card_id` 表达，schema 字段未变；按父子关系自动布局的客户端无需改动。

### 10.5 plan
```json
{
  "plan_id":   "pln_xxx",
  "goal":      "...",
  "strategy":  "sequential|parallel|mixed",
  "rationale": "...",
  "steps": [
    {"step_id": "s1", "subagent_type": "researcher", "depends_on": [],
     "user_facing_title": "...", "user_facing_summary": "..."}
  ]
}
```

### 10.6 step
```json
{
  "step_id":         "s1",
  "subagent_type":   "researcher",
  "status":          "queued|running",
  "input_summary":   "...",
  "output_summary":  "...",             // 仅 card.close
  "attempts":        1,
  "deliverables":    ["art_a1b2"],
  "artifacts":       [ ArtifactRef ]
}
```

### 10.7 artifact
```json
{
  "artifact_id":  "art_a1b2",
  "name":         "intern-schedule-email.md",
  "type":         "file|data|image|...",
  "mime_type":    "text/markdown",
  "size_bytes":   1240,
  "description":  "...",
  "role":         "draft_email|report|summary",
  "uri":          "file:///tmp/x.md",
  "version":      1,
  "thumbnail":    "<base64 png>"
}
```

#### ArtifactRef（嵌入式）
```json
{
  "artifact_id": "art_a1b2",
  "name":        "intern-schedule-email.md",
  "type":        "file",
  "mime_type":   "text/markdown",
  "size_bytes":  1240,
  "description": "...",
  "role":        "draft_email"
}
```

### 10.8 thinking / memory_op / budget / todo / team

参见 `internal/emit/v2/payload.go` 的 ThinkingPayload / MemoryOpPayload / BudgetPayload / TodoPayload / TeamPayload。

### 10.9 system

```json
{
  "summary":     "本次任务派到的 sub-agent (researcher) 依赖网络搜索，但配置中 web_search 和 tavily_search 均未启用，结果可能依赖训练知识、缺乏时效性和来源核查。",
  "action_hint": "如何启用（任一即可）:\n  • config.yaml: tools.web_search.enabled = true ...\n  • config.yaml: tools.tavily_search.enabled = true ..."
}
```

通用系统提示载荷。**没有**机器可读子类型字段（如 `notice_kind`）—— 各场景靠 `hint.title` / `hint.icon` 区分内容，后端日志由发出端的 zap 记录区分。

适用场景（持续扩充）：
- **搜索能力缺失**：`hint.icon=warning` / `hint.title="搜索能力不可用"`，由 `internal/engine/capability_gap_detector.go` 的 `SearchGapDetector` 在受影响 TierSubAgent 首次 spawn 时发出，session 级去重。
- (未来可扩) 配置缺失、密钥过期、限流降级等同类框架级通知。

**渲染契约：**
- `hint.title` 必有 —— 标识具体通知类别。
- `hint.icon` 为 `warning` 时按警告样式渲染；其它值（含空）走 registry 默认 `info` 视觉。
- `payload.action_hint` 若有，应展示为可读的修复指引（保留换行/项目符号）。
- card lifecycle `untracked` —— 无 `card.close`，不进入 orphan watchdog。

---

## 11. card.tick Inner Payloads（按 kind）

### kind=progress
```json
{"stage": "fetching", "progress_pct": 0.4, "items_processed": 12, "items_total": 30, "unit": "pages", "eta_ms": 18000}
```

### kind=heartbeat
```json
{"stage": "running_tools", "uptime_ms": 8500, "active_tool_card_id": "tool_x"}
```

### kind=intent
```json
{"intent": "正在搜索 vLLM 论文"}
```

### kind=note
```json
{"text": "skipped cache, fresh fetch", "severity": "info"}
```

### kind=escalation
```json
{"from_mode": "react", "to_mode": "plan", "reason": "复杂度超过 ReAct 阈值"}
```

---

## 12. ErrorInfo（统一错误模型）

```json
{
  "type":          "tool_timeout|orphan_timeout|rate_limit|overloaded|contract_fail|dependency_fail|user_aborted|permission_denied|max_turns|context_exceeded|model_error|budget_exhausted|invalid_input|internal",
  "code":          "BASH_TIMEOUT",       // 自由 machine code
  "message":       "Bash exceeded 120s",  // dev-facing
  "user_message":  "执行慢了点，我换个方式重试",  // L1 persona 转述
  "retryable":     true,
  "retry_after_ms": 5000,
  "recovery": {
    "action":       "retry|fallback|abort",
    "next_card_id": "step_b"             // 替换卡的指针
  }
}
```

错误**只**出现在 `card.close{status:failed}.payload.error` 或 `session.event{kind:error}.payload.inner.error`。

注册表（`internal/emit/v2/registry.go`）为每个 ErrorType 提供默认 `user_message` 和 `retryable`，业务调用 `NewError(typ, msg)` 自动填充。

---

## 13. Artifact 引用约定（协议级硬约束）

L1 persona（emma）在 `card.append(channel=text)` 内引用 artifact 时**必须**使用 markdown URI：

```
✅ 推荐：邮件已经准备好：[intern-schedule-email.md](artifact://art_a1b2)
❌ 禁止：邮件已经准备好：art_a1b2                              （让用户看到内部 ID）
❌ 禁止：邮件已经准备好：intern-schedule-email.md              （前端模糊匹配，多 artifact 同名失败）
```

**前端**识别 `artifact://` 协议，点击即跳转/打开右侧 artifact 卡片。

**强制机制**：emit Builder 在 message card append 时**自动改写**对 artifact 的提及。这条从 v1.x 的 prompt-side 软约束升级为协议级硬约束，prompt 漂移免疫。

---

## 14. Event Sequences（5 个真实场景）

### 14.1 纯文本对话
```
→ user.message            {content:{text:"Hi"}}
← session.event(opened)
← card.add  (turn,    "turn_c1")
← card.add  (message, "msg_1",  parent="turn_c1")
← card.append (msg_1, channel=text, chunk="Hello")
← card.append (msg_1, channel=text, chunk=" World!")
← card.close (msg_1, ok)
← card.close (turn_c1, ok, metrics={duration_ms:856})
```

### 14.2 服务端工具
```
→ user.message            {content:{text:"List files"}}
← card.add    (turn,    "turn_c1")
← card.add    (message, "msg_1",  parent="turn_c1")
← card.append (msg_1, text,       "Let me check:")
← card.append (msg_1, tool_input, "{\"command\":")
← card.append (msg_1, tool_input, "\"ls -la\"}")
← card.close  (msg_1, ok)
← card.add    (tool, "toolu_1", parent="msg_1",
                 payload:{name:bash, target:server, intent:"列目录"})
← card.close  (toolu_1, ok, payload.inner:{output:"...", render_hint:terminal})
← card.add    (message, "msg_2", parent="turn_c1")
← card.append (msg_2, text, "Here are the files:...")
← card.close  (msg_2, ok)
← card.close  (turn_c1, ok)
```

### 14.3 子 Agent
```
→ user.message            {content:{text:"Search auth bugs"}}
← card.add    (turn,    "turn_c1")
← card.add    (message, "msg_1", parent="turn_c1")
← card.append (msg_1, text, "Let me search...")
← card.close  (msg_1, ok)

← card.add    (tool, "toolu_1", parent="msg_1",  agent_id="main",
                 payload:{name:Agent, intent:"搜索 auth bug"})
← card.add    (agent, "sub_e5", parent="toolu_1", agent_id="sub_e5",
                 payload:{name:"search auth bugs", agent_type:sync})

# sub-agent 内部工具直接发顶层 card；envelope.agent_id="sub_e5"
← card.add    (tool, "toolu_sub_1", parent="sub_e5", agent_id="sub_e5",
                 payload:{name:Grep, target:server})
← card.tick   (toolu_sub_1, agent_id="sub_e5",
                 payload:{kind:heartbeat, inner:{stage:"searching", uptime_ms:5000}})
← card.tick   (toolu_sub_1, agent_id="sub_e5",
                 payload:{kind:progress, inner:{items_processed:12, items_total:30}})
← card.close  (toolu_sub_1, ok, agent_id="sub_e5", payload.inner:{output:"Found 3"})
← card.close  (sub_e5, ok, agent_id="sub_e5", metrics={duration_ms:8500})
← card.close  (toolu_1, ok, agent_id="main", payload.inner:{output:"Found 3..."})

← card.add    (message, "msg_2", parent="turn_c1")
← card.append (msg_2, text, "I found 3 potential...")
← card.close  (msg_2, ok)
← card.close  (turn_c1, ok)
```

**关键合约**（v2 协议核心）：sub-agent context 内**所有** card 的 `envelope.agent_id` 必须等于 `card.add(agent)` 时声明的 ID。emit Builder 的 `Sub()` 方法强制保证此约束——业务代码无逃逸路径。

### 14.4 Plan 模式 + 用户确认 + L3 契约（3 层嵌套）
```
→ user.message  {content:{text:"调研 X 写 Y"}, coordinator_mode:plan, plan_confirmation:required}

← card.add  (turn, "turn_c1")
← card.add  (message, "msg_1", parent="turn_c1")
← card.append (msg_1, text, "我来安排专业团帮你...")
← card.close  (msg_1, ok)

← card.add  (tool, "tu_main_1", parent="msg_1", payload:{name:Specialists, intent:"派 Specialists"})
← card.add  (agent, "sub_y", parent="tu_main_1", agent_id="sub_y", payload:{name:specialists})

← prompt.user  {payload:{request_id:"req_1", kind:plan_review, inner:{
                  plan_id:"pln_xxx", goal, steps:[
                    {id:"s1", description:"调研"},
                    {id:"s2", description:"撰写", depends_on:["s1"]}
                  ]
                }}}

→ prompt.user_response  {request_id:"req_1", decision:"approved", payload:{updated_steps:[...]}}

← prompt.reply  {payload:{request_id:"req_1", decision:"approved"}}

← card.add  (plan, "plan_pln_xxx", parent="sub_y", agent_id="sub_y",
              payload:{plan_id:"pln_xxx", goal, strategy:sequential, steps:[...]})
← card.add  (step, "step_s1", parent="plan_pln_xxx", agent_id="sub_y",
              payload:{step_id:"s1", subagent_type:researcher, status:queued})
← card.set  (step_s1, agent_id="sub_y", payload:{status:running})

← card.add  (agent, "sub_z", parent="step_s1", agent_id="sub_z",
              payload:{name:writer, agent_type:sync})
← card.add  (tool, "tu_l3_1", parent="sub_z", agent_id="sub_z",
              payload:{name:ArtifactWrite})
← card.close (tu_l3_1, ok, agent_id="sub_z", payload.inner:{
                artifacts:[{artifact_id:"art_a1b2", name:"intern-schedule-email.md",
                            type:file, role:draft_email}]
              })
← card.close (sub_z, ok, agent_id="sub_z")
← card.close (step_s1, ok, agent_id="sub_y", payload.inner:{deliverables:["art_a1b2"]})
← card.close (plan_pln_xxx, ok, agent_id="sub_y")
← card.close (sub_y, ok, agent_id="sub_y")
← card.close (tu_main_1, ok, agent_id="main", hint:{render_hint:specialist_summary})

← card.add    (message, "msg_2", parent="turn_c1")
← card.append (msg_2, text, "邮件已经准备好：[intern-schedule-email.md](artifact://art_a1b2)...")
← card.close  (msg_2, ok)
← card.close  (turn_c1, ok)
```

### 14.5 长任务沉默期可见进度
```
← card.add   (tool, "toolu_1", payload:{name:WebFetch, intent:"抓取 vLLM 论文"})
← card.tick  (toolu_1, payload:{kind:heartbeat, inner:{stage:"connecting",  uptime_ms:5000}})
← card.tick  (toolu_1, payload:{kind:heartbeat, inner:{stage:"fetching",    uptime_ms:10000}})
← card.tick  (toolu_1, payload:{kind:progress,  inner:{stage:"parsing", items_processed:12, items_total:30}})
← card.tick  (toolu_1, payload:{kind:progress,  inner:{stage:"parsing", items_processed:25, items_total:30, eta_ms:3000}})
← card.close (toolu_1, ok, metrics:{duration_ms:30200})
```

---

## 15. 客户端实现指南

### 15.1 状态机

客户端维护一棵卡片森林：

```typescript
type Card = {
  cardId: string
  parentCardId?: string
  cardKind: CardKind
  status: 'open' | 'ok' | 'failed' | 'skipped' | 'cancelled'
  payload: any                  // 累积型字段
  channels: Map<string, ChannelState>  // 流式累积
  ticks: TickEvent[]            // 节流型信号
  hint: Hint
  metrics?: Metrics
  events: number                // 收到的事件数
  agentId?: string
  updatedAt: number
}

type ChannelState = {
  chunks: string[]              // 按 index 排序后拼接 = 完整内容
  byIndex: Map<number, string>
}

const cards = new Map<string, Card>()
const tree = new Map<string, string[]>()  // parentCardId → cardIds[]
```

### 15.2 事件 dispatch（伪代码）

```typescript
function onEvent(ev: Event) {
  const { card_id, parent_card_id, card_kind, agent_id } = ev.envelope

  switch (ev.type) {
    case 'card.add': {
      cards.set(card_id, {
        cardId: card_id, parentCardId: parent_card_id, cardKind: card_kind,
        status: 'open', payload: ev.payload, channels: new Map(),
        ticks: [], hint: ev.hint, agentId: agent_id, events: 1,
        updatedAt: Date.now()
      })
      addToTree(parent_card_id, card_id)
      break
    }
    case 'card.set': {
      const c = cards.get(card_id); if (!c) return
      c.payload = { ...c.payload, ...ev.payload }
      c.events++; c.updatedAt = Date.now()
      break
    }
    case 'card.append': {
      const c = cards.get(card_id); if (!c) return
      const { channel, index, chunk, partial_json } = ev.payload
      const text = chunk ?? partial_json ?? ''
      let ch = c.channels.get(channel)
      if (!ch) { ch = { chunks: [], byIndex: new Map() }; c.channels.set(channel, ch) }
      const idx = index ?? 0
      ch.byIndex.set(idx, (ch.byIndex.get(idx) ?? '') + text)
      c.events++; c.updatedAt = Date.now()
      break
    }
    case 'card.tick': {
      const c = cards.get(card_id); if (!c) return
      c.ticks.push(ev.payload)
      // 限制 ticks 缓存上限：只保留最近 N 条
      if (c.ticks.length > 50) c.ticks.shift()
      c.updatedAt = Date.now()
      break
    }
    case 'card.close': {
      const c = cards.get(card_id); if (!c) return
      c.status = ev.payload.status
      if (ev.payload.error) c.payload.error = ev.payload.error
      if (ev.payload.inner) c.payload = { ...c.payload, ...ev.payload.inner }
      c.metrics = ev.metrics
      c.updatedAt = Date.now()
      break
    }
    case 'prompt.user':       /* show modal */ break
    case 'prompt.reply':      /* dismiss modal, play ack */ break
    case 'session.event':     /* router on payload.kind */ break
  }
}
```

### 15.3 卡片渲染

| CardKind  | 默认 React 组件 |
|---|---|
| turn      | `<Turn>`（顶层 timeline 容器） |
| message   | `<Message>`（聊天气泡，渲染 channels.text） |
| tool      | `<ToolCard>`（折叠面板，header=hint.title，body=payload.input + output） |
| agent     | `<AgentPanel>` |
| plan      | `<PlanAccordion>` |
| step      | `<PlanStepRow>` |
| artifact  | `<ArtifactChip>` |
| system    | `<SystemNotice>`（顶部通知条；`hint.icon=warning` 走警告变体；body=payload.summary + payload.action_hint） |
| ...       | ... |

### 15.4 Markdown 中的 artifact:// URI

```typescript
function renderMarkdown(text: string): JSX.Element {
  return <Markdown linkResolver={(href) => {
    if (href.startsWith('artifact://')) {
      const id = href.slice('artifact://'.length)
      return <ArtifactLink artifactId={id} />
    }
    return <a href={href} />
  }}>{text}</Markdown>
}
```

---

## 16. 错误处理

### 16.1 错误级别

| 级别 | 体现 | 客户端行为 |
|---|---|---|
| Card 级 | `card.close{status:failed}` | 卡片标红，显示 error.user_message + recovery.action |
| Session 级 | `session.event{kind:error}` | 全局通知，可能断开 |
| Connection 级 | WebSocket close (1001-1015) | 重连 |

### 16.2 客户端重试策略

| 触发 | 策略 |
|---|---|
| `error.retryable=true && retry_after_ms` | 指数退避，遵守 retry_after_ms |
| `error.retryable=true` | 立即重试一次，失败后退避 |
| `error.retryable=false` | 不自动重试，提示用户 |
| WebSocket 1011 / 1006 | 指数退避重连，最多 N 次 |

### 16.3 重连续传

见 §2.4。客户端缓存最近一段 `last_seq`，重连后发 `session.resume` 续传。

---

## 17. Type Reference 速查

### 17.1 Server Event Types
```
card.add | card.set | card.append | card.tick | card.close
prompt.user | prompt.reply
session.event
```

### 17.2 CardKind
```
turn | message | tool | agent | plan | step | artifact | thinking |
memory_op | budget | todo | team | system
```

### 17.3 TickKind
```
progress | heartbeat | intent | note | escalation
```

### 17.4 Channel
```
text | tool_input | thinking
```

### 17.5 ErrorType
```
tool_timeout | orphan_timeout | rate_limit | overloaded | contract_fail |
dependency_fail | user_aborted | permission_denied | max_turns |
context_exceeded | model_error | budget_exhausted | invalid_input | internal
```

### 17.6 AgentRole
```
persona | orchestrator | worker | system
```

### 17.7 Severity
```
info | warn | error
```

### 17.8 Status (card.close)
```
ok | failed | skipped | cancelled
```

---

## 18. 故障恢复实现参考（服务端）

恢复能力由四个层级配合实现：

| 层 | 文件 | 责任 |
|---|---|---|
| 持久化 | `internal/storage/sqlite/waits.go` | `pending_waits` 表 + CRUD + TTL sweep |
| 类型 | `internal/engine/wait/wait.go` | `PendingWait/Anchor/Answer/Store/Resumer` |
| Channel-side | `internal/channel/websocket/translator.go` + `conn.go` | emit 前持久化、reconnect 时重发、reply 双层路由（live → persisted）|
| Engine-side | `internal/engine/resume/resume.go` | `TextResumer`：合成续接 user.message 投给引擎 handler |

**关键不变性**：

1. **persist 先于 emit**——`translator.persistWait` 失败时不发送 prompt 帧（防止用户看到永远无法恢复的卡片）
2. **同一 request_id**：emit 和 reconnect 重发使用相同 ID（保证客户端去重）
3. **answer 路径单源真相**：translator 的 in-memory 映射（live）和 SQLite（persisted）作为路由的双重 source；live 命中也调 `Forget` 删 SQLite 行，防累积
4. **15d TTL janitor**：`runWaitJanitor` 每小时跑一次 `Prompter.SweepExpired`，用户永不返回的对话最长 15 天 + 1 小时后清理
5. **Forget 幂等**：重复回答只触发一次 Resume；二次回答收到 error 帧

测试覆盖见：
- `internal/storage/sqlite/waits_test.go`（CRUD / 并发 / TTL / 校验）
- `internal/engine/prompter/prompter_test.go`（live / restart / sweep / concurrent）
- `internal/channel/websocket/recovery_test.go`（**4 个真 WebSocket 端到端**：live answer / server restart replay / plan_review recovery / persist failure suppress）

---

## 19. 实现参考

- emit Builder（服务端）: `internal/emit/v2/builder.go`
- 注册表 + Hint 模板: `internal/emit/v2/registry.go`
- Lifecycle Watchdog: `internal/emit/v2/lifecycle.go`
- ErrorInfo: `internal/emit/v2/error.go`
- WS Sink: `internal/channel/websocket/v2/wire.go`
- 单元测试: `internal/emit/v2/*_test.go`
- 端到端测试（5 场景）: `internal/emit/v2/e2e_test.go`
- 设计文档: `docs/emit/2026-05-07-protocol-v2.2-card.md`
- 设计评审过程: `docs/emit/2026-05-07-protocol-review.md`
