# 架构二次评审报告

## 日期: 2026-04-05
## 评审人: Senior Go Architect (Second Review)

---

## 一、P0 修复验证

### P0-1: queryLoop 核心循环
**Status: ✅ 合格**

`internal/engine/queryloop.go` 实现了一个结构清晰的 5 阶段查询循环，与 TypeScript `src/query.ts` 的核心语义对齐良好。

**验证要点：**

1. **5 阶段完整性**: Phase 1 (预处理/auto-compact/max-turns检查) -> Phase 2 (LLM流式调用) -> Phase 3 (错误恢复/context取消检测) -> Phase 4 (工具执行) -> Phase 5 (续行判断) 全部实现。循环结构为 `for {}` 无限循环配合 return 退出，与 TS 的 `while(true)` + generator yield 模式在 Go 中的合理等价。

2. **10 Terminal Reasons**: `pkg/types/event.go:48-61` 定义了全部 10 种终止原因（`completed`, `aborted_streaming`, `aborted_tools`, `max_turns`, `prompt_too_long`, `blocking_limit`, `model_error`, `image_error`, `stop_hook_prevented`, `hook_stopped`），与 TS 原始定义完全一致。

3. **loopState**: 定义了 `turn`, `stopReason`, `lastUsage`, `cumulativeUsage` 四个字段。相比 TS 的 10 字段 `State` 是精简版本，但对于当前阶段足够。缺少 `maxOutputTokensRecoveryCount`, `hasAttemptedReactiveCompact` 等恢复状态字段，这意味着 Phase 3 的错误恢复路径尚未完整实现。

4. **ToolExecutor**: `executor.go` 实现了 parallel-read / serial-write 执行模型，包含权限检查、超时控制、panic recovery，质量良好。

5. **流式事件传播**: LLM 流式事件通过 `out chan<- types.EngineEvent` 实时传递，text delta, tool_start, tool_end 事件完整发出。

**遗留问题：**
- Phase 3 仅处理了 context 取消和通用 model error，缺少 prompt-too-long 自动压缩重试和 max_output_tokens 续行恢复。这两个恢复策略在 TS 代码中是生产稳定性的关键。当前标记为 P1 而非 P0，因为基本循环可以工作。
- `buildAssistantMessage` 正确构建了 assistant 消息，但没有处理 thinking blocks 的保留/剥离逻辑。
- 循环中 `stopReason == "end_turn" && len(toolCalls) > 0` 的续行逻辑正确映射了 TS 行为。

---

### P0-2: Router 反向数据流
**Status: ✅ 合格**

`internal/router/router.go` 完整修复了第一次评审中报告的"事件黑洞"问题。

**验证要点：**

1. **Channel 注册表**: Router 持有 `channels map[string]channel.Channel`，能通过 `msg.ChannelName` 查找来源 channel。
2. **事件转发**: `coreHandler` 遍历 `events` channel 并调用 `ch.SendEvent(ctx, msg.SessionID, &evt)`，实时将引擎事件推送给对应 channel。
3. **SendEvent 接口**: `channel.Channel` 接口新增了 `SendEvent(ctx, sessionID, event) error` 方法（第一次评审中指出缺失），完成了反向数据流的闭环。
4. **容错**: 未找到 channel 时会 drain events 以避免阻塞引擎 goroutine，并返回 `pkgerr.New(pkgerr.CodeNotFound, ...)`。单个 SendEvent 失败不中断流，仅记录日志后继续。
5. **健康检查**: Channel 接口增加了 `Health() error` 方法。

**遗留问题：**
- Router 在 `coreHandler` 中对 events 的消费是同步阻塞式的，如果 channel 的 `SendEvent` 实现缓慢，可能反压引擎。建议考虑 buffered 转发或超时保护。
- 缺少 channel 的动态注册/注销能力（当前 map 在构造时固定）。

---

### P0-3: 权限系统
**Status: ✅ 合格**

`internal/permission/` 包完整实现了核心权限模型，四个文件职责清晰。

**验证要点：**

1. **Mode（mode.go）**: 定义了 6 种模式 (`default`, `plan`, `accept_edits`, `bypass`, `dont_ask`, `auto`)，覆盖了 TS `types/permissions.ts` 中的 5 种外部模式 + `auto` 内部模式。缺少 `bubble` 模式，但 `bubble` 是 TS 内部专用的，Go 多通道场景可能不需要。含 `IsValid()` 验证方法。

2. **Rule（rule.go）**: 定义了 6 种 RuleSource（Session, CLIArg, LocalSettings, ProjectSettings, UserSettings, Policy），使用 `iota` 按优先级排序，与 TS 的 8 种 source 对比缺少 `flagSettings` 和 `command` 两种，但核心来源齐全。`bySourcePriority` 函数正确地按 source 优先级分组。

3. **Decision（decision.go）**: 三元决策 `Allow|Deny|Ask` + 6 种 Reason（rule, mode, read_only, bypass, hook, default），与 TS 的 `PermissionBehavior` 和 `PermissionDecisionReason` 对齐。

4. **Checker（checker.go）**: 评估逻辑三步走：bypass模式 -> 显式规则匹配（按优先级） -> 模式默认行为。`matchRule` 正确实现了 `bySourcePriority` 优先级扫描。各模式的默认行为与 TS 语义一致：
   - `default`: 读操作自动允许，写操作需确认
   - `plan`: 同 default
   - `accept_edits`: 读操作和文件编辑自动允许
   - `dont_ask`: 全部允许
   - `bypass`: 全部允许（通过 BypassChecker 快捷路径）

5. **集成**: `executor.go` 在工具执行前调用 `te.permChecker.Check()`，`main.go` 正确从配置构建 rules 并注入 checker。

**遗留问题：**
- `isFileEditTool` 使用字符串匹配（`strings.Contains(lower, "edit")`），比较脆弱。TS 中通过工具自身的 capability 声明来判断。建议在 Tool 接口上增加显式标记。
- `Ask` 决策当前被 executor 视为拒绝，缺少实际的用户确认流程。这在多通道服务场景下需要通过 channel 向用户发送确认请求。
- 规则中的 `Pattern` 字段标注为 "future use" 但未实现参数级匹配。

---

### P0-4: 重试引擎
**Status: ✅ 合格**

`internal/provider/retry/retry.go` 实现了一个功能完整的重试引擎，忠实再现了 TS `withRetry.ts` 的核心语义。

**验证要点：**

1. **默认配置**: `DefaultConfig()` 返回 `MaxRetries:10, InitialDelay:500ms, MaxDelay:32s, JitterFraction:0.25, FallbackAfter529:3`，与 TS 的 `DEFAULT_MAX_RETRIES=10, BASE_DELAY_MS=500, MAX_529_RETRIES=3` 精确对应。

2. **指数退避+抖动**: `calculateDelay` 实现 `500ms * 2^attempt`，cap at 32s，jitter `[-25%, +25%]`。实现正确，包含了负值保护。

3. **529 连续计数 + Fallback**: `consecutive529` 追踪连续 529 错误，达到阈值后返回 `FallbackTriggeredError`，与 TS 的 `FallbackTriggeredError` 语义一致。非 529 错误重置计数器。

4. **错误分类**: `ClassifyHTTPError` 正确映射了所有关键 HTTP 状态码：
   - 401 -> `ErrAuthFailed` (non-retryable)
   - 403 -> `ErrTokenRevoked` (non-retryable)
   - 413 -> `ErrPromptTooLong` (non-retryable)
   - 429 -> `ErrRateLimit` (retryable)
   - 529 -> `ErrOverloaded` (retryable)
   - 5xx -> `ErrServerError` (retryable)
   - `ClassifyNetworkError` -> retryable

5. **Context 取消**: 退避等待中通过 `select { case <-ctx.Done() }` 正确响应取消。

6. **非 API 错误短路**: `fn` 返回非 `*APIError` 类型的错误时立即返回，不重试。

**遗留问题：**
- `Retryer` 的 `consecutive529` 字段非线程安全。如果同一个 Retryer 实例被多个 goroutine 共享（例如多个 session 使用同一 provider），需要 atomic 操作或 mutex。当前设计假设每次调用链独占一个 Retryer 实例，需要在使用文档中明确。
- 缺少 TS 中的高级特性：Persistent Retry 模式（`CLAUDE_CODE_UNATTENDED_RETRY`）、Fast Mode cooldown、foreground/background 区分、`x-should-retry` header 解析。这些是 P2 级别的增量功能。
- `Retryer` 未被集成到 Provider 层——`main.go` 中的 placeholder provider 没有使用重试。需要在实现真实 provider 时集成。

---

### P0-5: main.go 完善
**Status: ✅ 合格**

`cmd/server/main.go` 从第一次评审时的 "7/11 步 TODO" 大幅改善，关键组件已连通。

**验证要点：**

1. **完整启动序列（11步）**: 配置加载 -> 日志初始化 -> 存储初始化 -> 事件总线 -> 工具注册 -> Provider初始化 -> Session Manager -> Engine -> Router -> Channels -> 信号等待。步骤 3-8 已从 TODO 变为实际代码。

2. **组件连接**: `NewQueryEngine` 正确注入了 provider, registry, sessionMgr, compactor, permChecker, bus, logger, config 全部依赖。Router 正确持有 engine 和 channels 引用。

3. **配置验证**: `cfg.Validate()` 在启动时调用，验证 port 范围、provider 非空、max_turns >= 1、compact threshold 0-1、storage 类型、permission mode 合法性。

4. **优雅关闭**: 实现了双信号支持（第一次优雅关闭，第二次强制退出）、带超时的 shutdown context（30s）、cleanup goroutine 取消、session 持久化、存储关闭、logger flush。

5. **Idle Cleanup**: `runIdleCleanup` goroutine 使用 `time.Ticker` 定期调用 `CleanupIdle`，修复了第一次评审中指出的缺失。

6. **Permission Checker 初始化**: `initPermissionChecker` 正确从配置构建 rules（allowed/denied tools），支持 bypass mode 快捷路径。

**遗留问题：**
- Provider 仍然是 placeholder 实现（关闭 channel 后立即返回 "not implemented" 错误）。这是预期的——真实 provider 实现是独立任务。
- 工具注册是空的（TODO 注释说明了注册模式），需要工具实现就绪后填充。
- Channel 启动/停止是 TODO，因为 channel 实现（Feishu/WebSocket/HTTP）尚未开发。
- 存储的 `Close()` 被调用了两次：一次在 `defer store.Close()`（第87行），一次在 shutdown 序列中（第220行）。虽然 `memory.Store.Close()` 是幂等的，但应该移除 defer 以避免重复调用。
- `initLogger` 中 level 解析失败时通过 stderr 输出警告，但返回的 logger 仍然是有效的（降级到 info），这是合理的。

---

## 二、整体架构评估

### 2.1 模块边界与依赖方向

**评级: ✅ 良好**

依赖图呈清晰的有向无环结构：

```
cmd/server/main.go
  ├── internal/config         (无内部依赖)
  ├── internal/engine         → provider, tool, session, compact, permission, event
  │     ├── compact           → provider
  │     └── session           → (定义 Store 接口，无实现依赖)
  ├── internal/router         → engine, channel, middleware
  │     └── middleware        → (仅依赖 pkg/types, pkg/errors)
  ├── internal/channel        → (仅依赖 pkg/types)
  ├── internal/tool           → provider, permission
  ├── internal/permission     → (无内部依赖)
  ├── internal/provider       → (仅依赖 pkg/types)
  │     └── retry             → (无内部依赖)
  ├── internal/event          → (无内部依赖)
  ├── internal/storage        → session (接口依赖)
  │     └── memory            → session
  ├── pkg/types               (无依赖)
  └── pkg/errors              (无依赖)
```

**优点：**
- `pkg/types` 和 `pkg/errors` 作为纯数据/错误定义层，无任何内部依赖，是稳定的底层抽象。
- `session.Store` 接口定义在消费方（session 包），而非实现方（storage 包），正确遵循了 Go 的接口所有权惯例，避免了循环依赖。
- `permission` 包完全独立，不依赖 tool 或 engine，可以被任何层使用。
- `provider/retry` 包独立于 provider 实现，可被任意 provider 复用。

**风险点：**
- `tool/registry.go` 导入了 `provider`（仅用于 `provider.ToolSchema` 类型），造成 tool -> provider 的耦合。`ToolSchema` 应该定义在 `pkg/types` 中以消除此依赖。
- `engine` 包直接依赖 `*session.Manager`（具体类型）而非接口，限制了测试替换能力。建议抽取 `SessionManager` 接口。

### 2.2 接口设计质量

**评级: ✅ 良好**

| 接口 | 方法数 | 评价 |
|------|--------|------|
| `engine.Engine` | 2 | 极简且充分。`ProcessMessage` 返回 event channel 是 Go 流式编程的惯用模式。 |
| `provider.Provider` | 3 | `Chat` + `CountTokens` + `Name`，覆盖核心需求。`ChatStream` 使用 `<-chan` + `Err func()` 模式是合理的流式封装。 |
| `tool.Tool` | 7 | 从第一次评审的 5 个方法扩展到 7 个（新增 `IsEnabled`, `IsConcurrencySafe`, `ValidateInput`），覆盖了第一次评审标记为 P0 的全部缺失方法。`BaseTool` 提供合理的默认实现。 |
| `channel.Channel` | 6 | 完整覆盖生命周期（Start/Stop）、双向通信（Send/SendEvent）、元信息（Name）、健康检查（Health）。 |
| `compact.Compactor` | 2 | `ShouldCompact` + `Compact` 简洁明了。 |
| `permission.Checker` | 1 | 单方法接口，极易 mock。`BypassChecker` 提供了零值实现。 |
| `session.Store` | 3 | CRUD 三方法，已有 `memory.Store` 作为内置实现。 |
| `storage.Storage` | 5 | 扩展了 `session.Store` 加上 `ListSessions` + `Close`，职责分明。 |
| `middleware.Handler/Middleware` | 函数类型 | 函数式中间件模式是 Go 的标准做法，`Chain` 组合器实现正确。 |

**改进建议：**
- `Tool.IsConcurrencySafe()` 当前无参数，但 TS 中的 `isConcurrencySafe(input)` 依赖输入来判断。例如 FileEdit 工具对不同路径的操作可能是并发安全的。建议考虑改为 `IsConcurrencySafe(input json.RawMessage) bool`。
- `ExecutionContext`（`pkg/types/context.go`）已定义但未被 `tool.Tool.Execute` 使用。工具当前只接收 `context.Context` + `json.RawMessage`，无法访问 session 状态、channel 名称等上下文信息。建议将 `ExecutionContext` 纳入工具调用路径。

### 2.3 并发安全性

**评级: ⚠️ 有已知风险，但总体可控**

**已解决（相比第一次评审）：**
- `compact.LLMCompactor.failureCount` 已改为 `atomic.Int32`，修复了第一次评审 REVIEW_TESTABILITY.md 中的 P1 风险。
- `event.Bus.PublishAsync` 已增加 `recover()` panic recovery，修复了 R6 风险。
- Rate limit middleware 增加了 lazy cleanup（每 100 个请求清理过期 bucket），缓解了内存泄漏风险 R8。

**遗留并发风险：**

| 风险 | 严重度 | 位置 | 描述 |
|------|--------|------|------|
| 嵌套锁 | 中 | `session/manager.go:48-53` | `GetOrCreate` 持有 manager 写锁时获取 session 写锁。当前代码中锁获取顺序一致（manager -> session），但缺乏编译期保证，新代码可能引入反向获取。 |
| I/O 持锁 | 中 | `session/manager.go:131` | `CleanupIdle` 持有 manager 写锁时调用 `store.SaveSession`。如果存储是网络存储（SQLite + WAL、远程 DB），所有 manager 操作将被阻塞。 |
| 指针共享 | 低 | `memory/memory.go:28` | `SaveSession` 存储指针引用。活跃 session 和存储中的 session 是同一对象，修改立即可见。对 memory store 功能正确，但语义上不是持久化快照。 |
| Retryer 非线程安全 | 低 | `retry/retry.go:88` | `consecutive529` 字段无同步保护。如果 Retryer 被多 goroutine 共享需要加锁或使用 atomic。 |
| executor 事件发送 | 低 | `executor.go:104,112` | 多个并行工具执行的 goroutine 同时向 `out chan<-` 发送事件。channel 本身是安全的，但事件的到达顺序不确定。对 streaming 场景这是可接受的。 |

**总体评估**: 对于早期原型阶段，并发安全水平可以接受。进入生产前需要重构 `CleanupIdle` 为"先收集-后操作"模式（收集阶段持读锁，操作阶段逐个加锁并释放 manager 锁）。

### 2.4 错误处理模式

**评级: ✅ 良好**

1. **领域错误体系**: `pkg/errors` 定义了 16 个错误码，覆盖核心场景。`DomainError` 实现了 `error` + `Unwrap()` 接口，支持 `errors.Is/As` 链式解包。提供了 6 个 sentinel error 变量用于快速比较。

2. **API 错误分类**: `retry.APIError` 独立于 domain error，包含 `StatusCode`, `Type`, `Retryable`, `Raw` 四维信息，分类准确。`ClassifyHTTPError` 和 `ClassifyNetworkError` 提供了便捷的错误构建。

3. **错误传播**: 引擎层通过 `types.Terminal` 传达终止原因和错误信息，不丢失上下文。工具执行器的每个错误路径（unknown tool, disabled, validation fail, permission denied, execution error, panic）都正确返回 `ToolResult{IsError: true}`，让 LLM 能看到错误信息。

4. **Panic Recovery**: `executor.go:120-131` 在每个工具执行中添加了 defer recover，防止单个工具 panic 崩溃整个引擎。

**改进建议：**
- `retry.APIError` 和 `pkg/errors.DomainError` 是两套独立的错误体系。建议在 provider 层实现中将 `APIError` 转换为 `DomainError`，统一错误处理路径。
- 引擎的 `ProcessMessage` 返回 `error` 但错误场景有限（仅 session get-or-create 失败）。大部分错误通过 event channel 的 `EngineEventError` 和 `Terminal` 传递，这是合理的异步错误模型。

### 2.5 TypeScript 忠实度

对四个关键模块的忠实度评估：

#### `src/query.ts` -> `internal/engine/queryloop.go`

| TS 概念 | Go 实现 | 忠实度 |
|---------|---------|--------|
| 5 阶段循环 | ✅ 完整实现 | 高 |
| `State` (10 字段) | `loopState` (4 字段) | 中 — 缺少恢复计数器 |
| `QueryParams` | `QueryEngineConfig` | 中 — 缺少 per-query params |
| 10 Terminal Reasons | ✅ 全部定义 | 高 |
| `AsyncGenerator yield` | `chan<- EngineEvent` | 高 — Go 惯用等价 |
| prompt-too-long recovery | ❌ 未实现 | 低 |
| max_output_tokens recovery | ❌ 未实现 | 低 |
| fallback model switch | ❌ 未实现 | 低 |
| `StreamingToolExecutor` | `ToolExecutor.ExecuteBatch` | 中 — 非流式但并发 |
| `getAttachmentMessages` | ❌ 未实现 | N/A (多通道不需要) |
| `autoCompactTracking` | `compactor.ShouldCompact` | 中 — 简化但可用 |
| thinking block rules | ❌ 未处理 | 低 |
| `buildPostCompactMessages` | `sess.SetMessages` | 中 — 简化 |
| tool-result pairing | ✅ 在循环中正确配对 | 高 |

**总体**: 核心循环骨架忠实度高，错误恢复路径忠实度低。对于早期原型这是合理的优先级选择。

#### `src/Tool.ts` -> `internal/tool/tool.go`

| TS 概念 | Go 实现 | 忠实度 |
|---------|---------|--------|
| `name` | `Name()` | 高 |
| `call()` / execution | `Execute()` | 高 |
| `isReadOnly()` | `IsReadOnly()` | 高 |
| `isEnabled()` | `IsEnabled()` | 高 (新增) |
| `isConcurrencySafe()` | `IsConcurrencySafe()` | 中 — 无参数版本 |
| `validateInput()` | `ValidateInput()` | 高 (新增) |
| `inputSchema` | `InputSchema()` | 高 |
| `description` | `Description()` | 高 |
| `aliases` | ❌ | 低 |
| `isDestructive()` | ❌ | N/A |
| `checkPermissions()` | 通过 `permission.Checker` 外部化 | 中 — 不同架构但等价 |
| `ToolUseContext` (40+ 字段) | `types.ExecutionContext` (已定义未集成) | 低 |
| `BaseTool` defaults | `BaseTool` struct embedding | 高 — Go 惯用等价 |

**总体**: 核心工具抽象忠实度高，高级功能（aliases, destructive, context）缺失。

#### `src/types/permissions.ts` -> `internal/permission/`

| TS 概念 | Go 实现 | 忠实度 |
|---------|---------|--------|
| 7 PermissionModes | 6 Modes (缺 `bubble`) | 高 |
| 8 RuleSources | 6 Sources (缺 `flagSettings`, `command`) | 中 |
| `allow\|deny\|ask` 三元决策 | `Allow\|Deny\|Ask` | 高 |
| 11 DecisionReasons | 6 Reasons | 中 |
| Rule 优先级排序 | `bySourcePriority` iota 排序 | 高 |
| `PermissionRule{source, behavior, value}` | `Rule{Source, Behavior, ToolName, Pattern}` | 高 |
| 5-way resolveOnce race | ❌ | 低 — 多通道需要异步确认 |
| permission hooks | ❌ (Reason 中有 `hook` 但无实现) | 低 |
| classifier (auto mode) | ❌ (Mode 已定义但无实现) | 低 |

**总体**: 权限模型框架忠实度高，交互式确认和自动分类缺失。

#### `src/services/api/withRetry.ts` -> `internal/provider/retry/retry.go`

| TS 概念 | Go 实现 | 忠实度 |
|---------|---------|--------|
| Normal mode (10 retries) | ✅ `DefaultConfig` | 高 |
| Exponential backoff + jitter | ✅ `calculateDelay` | 高 |
| 529 consecutive -> fallback | ✅ `FallbackTriggeredError` | 高 |
| Non-retryable short circuit | ✅ `!apiErr.Retryable` | 高 |
| HTTP error classification | ✅ `ClassifyHTTPError` | 高 |
| Network error retryable | ✅ `ClassifyNetworkError` | 高 |
| Context cancellation | ✅ `select <-ctx.Done()` | 高 |
| Persistent retry mode | ❌ | 低 |
| Fast mode cooldown | ❌ | 低 |
| Foreground/background 区分 | ❌ | 低 |
| `x-should-retry` header | ❌ | 低 |
| OAuth 401 refresh | ❌ | 低 |
| ECONNRESET disable keep-alive | ❌ | 低 |

**总体**: 核心重试机制忠实度高（normal mode 完整复现），高级模式缺失。

---

## 三、剩余问题清单

| ID | 严重级别 | 模块 | 问题描述 | 建议修复方案 |
|----|---------|------|---------|-------------|
| S1 | **P1** | engine | prompt-too-long 恢复未实现 — 长对话中 API 返回 413 时引擎直接终止，而非尝试自动压缩重试 | 在 Phase 3 中检测 `TerminalPromptTooLong`，调用 `compactor.Compact` 后重试 LLM 调用 |
| S2 | **P1** | engine | max_output_tokens 续行未实现 — LLM 输出截断时无法自动注入续行消息并继续 | 在 Phase 3 中检测 `stop_reason == "max_tokens"`，注入 meta 消息要求 LLM 从断点继续，最多重试 3 次 |
| S3 | **P1** | tool | `ExecutionContext` 已定义但未集成到工具调用路径 — 工具无法访问 session 状态、channel 信息等上下文 | 将 `ExecutionContext` 注入 `Tool.Execute` 或通过 `context.Context` 的 Value 传递 |
| S4 | **P1** | tool/registry | `registry.go` 导入 `provider` 包（仅用于 `ToolSchema`）— 不必要的跨层耦合 | 将 `ToolSchema` 移至 `pkg/types` 中 |
| S5 | **P1** | engine | `QueryEngine` 直接依赖 `*session.Manager` 具体类型 — 限制测试替换 | 抽取 `SessionProvider` 接口：`GetOrCreate`, `Get` |
| S6 | **P1** | provider | 重试引擎 (`retry.Retryer`) 未集成到 Provider 调用路径 | 在实现真实 Provider 时，将 `Retryer.Do` 包裹 `Chat` 调用 |
| S7 | **P2** | session/manager | `CleanupIdle` 持有 manager 写锁时执行 I/O（`store.SaveSession`）— 阻塞所有 session 操作 | 重构为先收集待归档 session ID（持读锁），释放锁后逐个归档（持写锁仅在 delete 时） |
| S8 | **P2** | session/manager | `GetOrCreate` 嵌套锁（manager写锁 -> session写锁）— 虽然当前无反向路径，但脆弱 | 改为释放 manager 锁后再修改 session 状态，或使用一致的锁协议文档 |
| S9 | **P2** | main.go | `store.Close()` 被调用两次（defer + shutdown 序列）| 移除第87行的 `defer store.Close()`，仅在 shutdown 序列中关闭 |
| S10 | **P2** | retry | `Retryer.consecutive529` 非线程安全 — 多 goroutine 共享 Retryer 时有数据竞争 | 改为 `atomic.Int32` 或在文档中明确"每调用链一个实例" |
| S11 | **P2** | engine | 缺少 fallback model 切换 — 连续 529 后无降级路径 | 在 `QueryEngine` 中添加 fallback provider，当 `FallbackTriggeredError` 发生时切换 |
| S12 | **P2** | permission | `isFileEditTool` 字符串匹配脆弱 | 在 `Tool` 接口上增加 `IsFileEditor() bool` 方法或使用 tool metadata 标签 |
| S13 | **P2** | compactor | `ShouldCompact` 基于 `msg.Tokens` 但 token 计数依赖 provider 的 `CountTokens` — 当前 placeholder provider 返回 0 | 在工具执行和 LLM 响应中正确填充 `Message.Tokens` 字段，或在 `ShouldCompact` 中使用 message count 作为后备 |
| S14 | **P2** | event/bus | `Publish` 同步调用所有 handler — 一个慢 handler 阻塞整个引擎事件发布 | 对关键路径（query.started, query.completed）使用 `PublishAsync`，或设置 handler 超时 |
| S15 | **P3** | config | 缺少 `ThinkingConfig`、`CacheControl`、`FallbackModel` 配置 | 扩展 `EngineConfig` 和 `LLMConfig` 配置结构 |
| S16 | **P3** | types | `IncomingMessage` 仅支持 `Text` 字段 — 不支持图片、文件等多模态内容 | 改为 `Content []ContentBlock` 以支持多模态 |
| S17 | **P3** | router | 缺少 channel 动态注册/注销 | 为 Router 添加 `RegisterChannel`/`UnregisterChannel` 方法（加锁） |
| S18 | **P3** | engine | 缺少 thinking block 保留/剥离逻辑 | 当集成支持 thinking 的 provider 时，在 `buildAssistantMessage` 中处理 thinking blocks |

---

## 四、结论与建议

### 总体评估

**P0 修复验证结果: 5/5 全部通过。** 第一次评审中标记的 5 个 P0 问题均已得到有效修复：

- queryLoop 核心循环从零实现到了 5 阶段完整循环，包含 10 种终止原因和并发工具执行。
- Router 反向数据流已完全打通，Channel 可以接收流式引擎事件。
- 权限系统从无到有建立了 Mode-Rule-Decision-Checker 四层模型。
- 重试引擎忠实再现了 TS 的核心重试语义（指数退避、529 fallback）。
- main.go 从大量 TODO 变为可组装的完整启动序列。

### 架构成熟度评估

| 维度 | 评分 (1-5) | 说明 |
|------|-----------|------|
| 模块边界 | 4.5 | 依赖方向清晰，接口定义在消费方，仅 tool->provider 有一处不必要耦合 |
| 接口设计 | 4.0 | 核心接口简洁且方法数合理，`BaseTool` 默认实现是好模式。缺少 `SessionProvider` 接口 |
| 并发安全 | 3.0 | Session 和 Manager 的锁策略需要重构（嵌套锁、I/O 持锁）。Compactor 的 atomic 修复是正确的 |
| 错误处理 | 4.0 | 双层错误体系（DomainError + APIError）完整，sentinel errors 可用。工具 panic recovery 到位 |
| TS 忠实度 | 3.5 | 核心骨架高度忠实，恢复路径和高级特性缺失但对原型阶段合理 |
| 可测试性 | 4.0 | 所有核心依赖通过接口注入，`AllowAllChecker` 和 `memory.Store` 提供了测试友好的默认实现 |
| 生产就绪度 | 2.0 | Provider、Tool 实现、Channel 实现均为 placeholder，距离能处理真实 LLM 请求还有显著距离 |

### 建议的下一步优先级

**Phase 1 (P1 — 使核心循环健壮):**
1. 实现 prompt-too-long 恢复（S1）
2. 实现 max_output_tokens 续行（S2）
3. 集成 `ExecutionContext` 到工具调用（S3）
4. 将 `ToolSchema` 移至 `pkg/types`（S4）
5. 抽取 `SessionProvider` 接口（S5）

**Phase 2 (P1-P2 — 连接真实组件):**
6. 实现 Anthropic API Provider（集成 retry engine）
7. 实现至少一个 Channel（HTTP REST 最简单）
8. 实现核心工具（Bash, FileRead, FileEdit）
9. 重构 session Manager 锁策略（S7, S8）

**Phase 3 (P2-P3 — 生产加固):**
10. Fallback model 切换（S11）
11. Thinking block 支持（S18）
12. 多模态消息支持（S16）
13. 可观测性（OpenTelemetry 集成）
14. 端到端集成测试

### 最终结论

Go 重建项目已从"骨架设计"阶段成功推进到"核心引擎可组装"阶段。5 个 P0 修复质量良好，代码风格符合 Go 惯例，架构分层清晰。当前状态是一个 **架构正确但功能不完整的原型**——所有抽象层已连通，但真实实现（Provider、Tool、Channel）仍需填充。18 个遗留问题中无 P0 级别阻塞项，可以有序推进。
