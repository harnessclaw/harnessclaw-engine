# 引擎层架构评审报告

> 评审人: 资深架构师 | 日期: 2026-04-05

## 评审总结

| 指标 | 数量 |
|------|------|
| 覆盖项 (✅) | 2/10 |
| 部分覆盖 (⚠️) | 4/10 |
| 缺失项 (❌) | 4/10 |
| **整体评级** | **需改进** |

Go Rebuild 的分层架构设计（Channel → Router → Engine → Provider/Tool）和基础 interface 定义方向正确，但引擎层核心——即 queryLoop 循环——目前仅定义了 interface 签名，缺少实际实现。原始 TypeScript 源码中 `query.ts` 约 1700+ 行的核心循环逻辑（五阶段处理、10 种终止原因、三级压缩策略、流式恢复机制等），在 Go 版本中几乎完全没有对应的实现代码。

---

## 逐项评审

### 1. queryLoop 五阶段循环完整性

**评级**: ❌ 缺失

**原始设计**:
`src/query.ts` 实现了一个精密的五阶段异步生成器循环（约 1700 行）：
1. **Phase 1 — 预处理**: `applyToolResultBudget()` → `snipCompactIfNeeded()` → `deps.microcompact()` → `applyCollapsesIfNeeded()` → `deps.autocompact()` → 阻塞限制检查
2. **Phase 2 — LLM API 调用**: 配置 `StreamingToolExecutor`、模型解析、流式调用 `deps.callModel()`，流中识别 `tool_use` 块，支持 fallback 模型切换
3. **Phase 3 — 后处理恢复**: prompt-too-long 恢复（context collapse drain → reactive compact）、max_output_tokens 恢复（escalate → multi-turn recovery，上限 3 次）、fallback model 重试
4. **Phase 4 — 工具执行**: `StreamingToolExecutor.getRemainingResults()` 或 `runTools()` 串行执行，收集 `toolResults`，检查 abort 信号和 hook 阻止
5. **Phase 5 — 附件与续行**: `getAttachmentMessages()`（内存预取消费、skill discovery、queued commands）、maxTurns 检查、更新 state 后 `continue`

关键状态结构 `State` 包含 10 个字段：`messages`, `toolUseContext`, `autoCompactTracking`, `maxOutputTokensRecoveryCount`, `hasAttemptedReactiveCompact`, `maxOutputTokensOverride`, `pendingToolUseSummary`, `stopHookActive`, `turnCount`, `transition`。

**Go Rebuild 现状**:
`internal/engine/engine.go` 仅定义了 `Engine` interface（两个方法：`ProcessMessage` 和 `AbortSession`），**没有任何循环实现**。架构文档 `ARCHITECTURE.md` 中提到 "核心循环: 预处理→LLM→工具→续行"，但停留在概念描述层面。

**差距分析**:
- 五阶段循环完全没有代码实现
- 缺少 `State` 等价的循环状态结构
- 缺少 `QueryParams` 等价的输入参数定义
- 缺少循环内每个阶段的具体逻辑

**改进建议**:
```go
// 建议实现 internal/engine/queryloop.go
type LoopState struct {
    Messages                    []types.Message
    ToolUseContext              *ToolUseContext
    AutoCompactTracking         *AutoCompactTrackingState
    MaxOutputTokensRecoveryCount int
    HasAttemptedReactiveCompact bool
    MaxOutputTokensOverride     *int
    TurnCount                   int
    Transition                  *ContinueReason
}

func (e *queryEngine) runQueryLoop(ctx context.Context, params *QueryParams) <-chan types.EngineEvent {
    ch := make(chan types.EngineEvent)
    go func() {
        defer close(ch)
        state := initLoopState(params)
        for {
            // Phase 1: Preprocess
            msgs := e.preprocess(ctx, state)
            // Phase 2: LLM Call
            assistant, toolBlocks, err := e.callLLM(ctx, msgs, state)
            // Phase 3: Recovery
            if recovery := e.recover(ctx, assistant, state); recovery != nil {
                state = recovery; continue
            }
            // Phase 4: Tool Execution
            if len(toolBlocks) > 0 {
                results := e.executeTools(ctx, toolBlocks, state)
                // Phase 5: Continuation
                state = e.continueLoop(ctx, state, assistant, results)
                continue
            }
            // Terminal
            break
        }
    }()
    return ch
}
```

---

### 2. Terminal Reasons 完整性

**评级**: ❌ 缺失

**原始设计**:
`src/query.ts` 定义了 10 种终止原因（`Terminal` 类型）：

| 原因 | 触发条件 | 代码位置 |
|------|----------|----------|
| `completed` | 模型自然结束，无 tool_use | query.ts:1357 |
| `aborted_streaming` | 流式过程中 AbortController 触发 | query.ts:1051 |
| `aborted_tools` | 工具执行过程中 abort | query.ts:1515 |
| `max_turns` | `turnCount >= maxTurns` | query.ts 续行部分 |
| `prompt_too_long` | API 413 + reactive compact 失败 | query.ts:1175 |
| `blocking_limit` | token 达到阻塞限制（auto-compact 关闭时） | query.ts:646 |
| `model_error` | 不可恢复的 API 错误 | query.ts:996 |
| `image_error` | ImageSizeError / ImageResizeError | query.ts:977 |
| `stop_hook_prevented` | Stop hook 返回 `{stop: true}` | query.ts:1279 |
| `hook_stopped` | hook_stopped_continuation 附件 | query.ts:1521 |

**Go Rebuild 现状**:
`pkg/types/event.go` 定义了 5 种 `EngineEventType`（text, tool_start, tool_end, error, done），但这是事件类型，**不是终止原因**。没有等价的 `TerminalReason` 类型定义。

**差距分析**:
- 完全缺少终止原因枚举
- `EngineEvent.Type` 混淆了流式事件类型和循环终止原因
- 没有区分"正常完成"与各种异常终止

**改进建议**:
```go
type TerminalReason string

const (
    TerminalCompleted        TerminalReason = "completed"
    TerminalAbortedStreaming  TerminalReason = "aborted_streaming"
    TerminalAbortedTools     TerminalReason = "aborted_tools"
    TerminalMaxTurns         TerminalReason = "max_turns"
    TerminalPromptTooLong    TerminalReason = "prompt_too_long"
    TerminalBlockingLimit    TerminalReason = "blocking_limit"
    TerminalModelError       TerminalReason = "model_error"
    TerminalImageError       TerminalReason = "image_error"
    TerminalStopHookPrevented TerminalReason = "stop_hook_prevented"
    TerminalHookStopped      TerminalReason = "hook_stopped"
)

type Terminal struct {
    Reason TerminalReason
    Error  error // 当 Reason 为 model_error 时携带原始错误
}
```

---

### 3. 流式响应与错误恢复策略

**评级**: ❌ 缺失

**原始设计**:
`query.ts` 实现了三种核心恢复策略：

**a) prompt-too-long 自动压缩重试（query.ts:1085-1183）**:
- 首先尝试 context collapse drain（`contextCollapse.recoverFromOverflow()`）
- 若失败或未启用，尝试 reactive compact（`reactiveCompact.tryReactiveCompact()`）
- 成功后用 `buildPostCompactMessages()` 替换消息，`state.transition = { reason: 'reactive_compact_retry' }`
- 失败则 surface 错误并终止

**b) max_output_tokens 续行（query.ts:1188-1256）**:
- 首先尝试 escalate：若未手动设置 `maxOutputTokensOverride`，升级到 `ESCALATED_MAX_TOKENS`(64k)
- 若 escalate 失败或已执行，进入 multi-turn recovery：注入 meta 消息 "Output token limit hit. Resume directly..."
- 最多重试 `MAX_OUTPUT_TOKENS_RECOVERY_LIMIT`(3) 次
- 恢复耗尽后 surface 原始错误

**c) 529 fallback model 切换（query.ts:893-953）**:
- 捕获 `FallbackTriggeredError`
- 清空 `assistantMessages`，tombstone 孤儿消息
- 切换到 `fallbackModel`，剥离 thinking signature blocks
- 创建新的 `StreamingToolExecutor`，重试

**Go Rebuild 现状**:
无任何恢复逻辑。`internal/engine/engine.go` 只有两行 interface 方法。Provider 层的 `ChatStream` 没有错误分类机制。

**差距分析**:
- 完全缺少 prompt-too-long 检测和恢复
- 完全缺少 max_output_tokens 续行机制
- 完全缺少 fallback model 切换
- 缺少流式错误的 withheld 机制（先收集错误、尝试恢复、恢复失败才 surface）

**改进建议**:
需要在引擎循环中实现完整的错误分类和恢复链。建议定义：
```go
type APIError struct {
    Type    APIErrorType // prompt_too_long, max_output_tokens, rate_limit, overloaded, etc.
    Message string
    Raw     error
}

func (e *queryEngine) tryRecover(ctx context.Context, apiErr *APIError, state *LoopState) (*LoopState, bool) {
    switch apiErr.Type {
    case ErrPromptTooLong:
        return e.recoverPromptTooLong(ctx, state)
    case ErrMaxOutputTokens:
        return e.recoverMaxOutputTokens(ctx, state)
    default:
        return nil, false
    }
}
```

---

### 4. QueryDeps 依赖注入模式

**评级**: ⚠️ 部分覆盖

**原始设计**:
`src/query/deps.ts` 定义了 `QueryDeps` 接口：
```typescript
type QueryDeps = {
    callModel: typeof queryModelWithStreaming
    microcompact: typeof microcompactMessages
    autocompact: typeof autoCompactIfNeeded
    uuid: () => string
}
```
`productionDeps()` 返回真实实现，测试注入 fakes。`query()` 通过 `params.deps ?? productionDeps()` 获取依赖。

**Go Rebuild 现状**:
Go 版本通过 interface 实现了天然的 DI——`Engine` interface、`Compactor` interface、`Provider` interface、`PermissionChecker` interface 都是依赖抽象。`LLMCompactor` 通过构造函数注入 `provider.Provider`。

**差距分析**:
- ✅ Go 的 interface 设计天然支持依赖注入，方向正确
- ⚠️ 缺少一个聚合的 "QueryDeps" 等价物，将 `callModel`、`microcompact`、`autocompact`、`uuid` 四个依赖统一注入
- ⚠️ Engine interface 目前没有构造函数或 factory 函数展示如何组装依赖
- ⚠️ 缺少 `QueryConfig`（不可变的每次查询配置快照）等价物

**改进建议**:
```go
type QueryDeps struct {
    CallModel   func(ctx context.Context, req *ChatRequest) (*ChatStream, error)
    Microcompact func(ctx context.Context, msgs []types.Message) ([]types.Message, error)
    Autocompact  func(ctx context.Context, msgs []types.Message, tracking *AutoCompactState) (*CompactionResult, error)
    NewUUID     func() string
}

func NewQueryEngine(deps QueryDeps, registry *tool.Registry, compactor compact.Compactor) Engine {
    return &queryEngine{deps: deps, registry: registry, compactor: compactor}
}
```

---

### 5. 工具执行并发控制

**评级**: ⚠️ 部分覆盖

**原始设计**:
- `StreamingToolExecutor`（`src/services/tools/StreamingToolExecutor.ts`）：流式中即开始执行只读工具
- `runTools()`（`src/services/tools/toolOrchestration.ts`）：串行执行所有工具
- 关键方法 `tool.isConcurrencySafe(input)` 决定是否可并发
- `tool.isReadOnly(input)` 用于权限和并发决策
- 超时通过 `AbortController` + `context.WithTimeout` 等价逻辑控制

**Go Rebuild 现状**:
- `tool.Tool` 接口定义了 `IsReadOnly() bool`，方向正确
- `ARCHITECTURE.md` 明确提到 "只读工具可并发执行，写入工具串行执行，带超时控制"
- `UML_FLOWS.md` 展示了并发/串行分支的流程图
- 但**没有实际的 executor 实现代码**

**差距分析**:
- ✅ Interface 设计已包含 `IsReadOnly()` 用于并发决策
- ✅ 架构文档明确了并发策略
- ⚠️ 缺少 `IsConcurrencySafe(input)` — 原始代码中 `isConcurrencySafe` 是独立于 `isReadOnly` 的，某些只读操作也可能不是并发安全的
- ❌ 缺少实际的并发执行器实现（等价于 `StreamingToolExecutor` 或 `runTools`）
- ❌ 工具超时由 `engine.tool_timeout` 配置但没有执行代码

**改进建议**:
```go
type ToolExecutor struct {
    registry *Registry
    checker  PermissionChecker
    timeout  time.Duration
}

func (e *ToolExecutor) ExecuteBatch(ctx context.Context, calls []types.ToolCall) []types.ToolResult {
    var readOnly, writeOnly []types.ToolCall
    for _, c := range calls {
        t := e.registry.Get(c.Name)
        if t != nil && t.IsReadOnly() {
            readOnly = append(readOnly, c)
        } else {
            writeOnly = append(writeOnly, c)
        }
    }
    // Parallel execution for read-only
    g, gctx := errgroup.WithContext(ctx)
    results := make([]types.ToolResult, len(readOnly))
    for i, c := range readOnly {
        i, c := i, c
        g.Go(func() error {
            tctx, cancel := context.WithTimeout(gctx, e.timeout)
            defer cancel()
            r, err := e.registry.Get(c.Name).Execute(tctx, c.Input)
            // ...
        })
    }
    // Serial execution for writes
    for _, c := range writeOnly {
        tctx, cancel := context.WithTimeout(ctx, e.timeout)
        // ...
    }
}
```

---

### 6. prompt-cache 稳定性

**评级**: ✅ 已覆盖

**原始设计**:
`src/tools.ts` 的 `assembleToolPool()` 函数（:345-367）：
- 内置工具按 `name.localeCompare(name)` 排序，形成连续前缀
- MCP 工具也排序，追加在内置工具之后
- 用 `uniqBy(..., 'name')` 去重，内置工具优先
- 目的：确保 Anthropic API 的 prompt cache key 在 MCP 工具变化时保持稳定

**Go Rebuild 现状**:
`internal/tool/registry.go` 的 `All()` 方法（:44-56）：
```go
sort.Slice(result, func(i, j int) bool {
    return result[i].Name() < result[j].Name()
})
```
返回按名称排序的工具列表。

**差距分析**:
- ✅ 工具按名称排序，保证稳定性
- ⚠️ 缺少 "内置工具作为连续前缀 + MCP 工具追加" 的两段式排序
- ⚠️ 当前 Registry 不区分内置工具和 MCP 工具

**改进建议**:
如果将来支持 MCP 工具，需要在 Registry 中标记工具来源，并在 `Schemas()` 中实现两段式排序：
```go
func (r *Registry) SchemasForCache() []provider.ToolSchema {
    builtIn, mcp := r.partitionBySource()
    sortByName(builtIn)
    sortByName(mcp)
    return append(toSchemas(builtIn), toSchemas(mcp)...)
}
```

---

### 7. 上下文压缩三级策略

**评级**: ⚠️ 部分覆盖

**原始设计**:
文档 `docs/14-compact-service/README.md` 描述了三级策略：
1. **Tier 1 — Micro-compact**（每次查询前运行）：基于时间的工具结果清理 + cache-editing API 路径
2. **Tier 2 — Session Memory Compact**：利用已有的 session memory 作为摘要，轻量级
3. **Tier 3 — Full Compact**：通过 forked agent 生成会话摘要，重量级
- **Circuit Breaker**: 连续失败 3 次后停止自动压缩
- **阈值计算**: `effectiveContextWindow = contextWindow - min(maxOutputTokens, 20000)`，`autoCompactThreshold = effectiveContextWindow - 13000`

**Go Rebuild 现状**:
`internal/engine/compact/compactor.go` 实现了：
- ✅ `Compactor` interface 定义（`ShouldCompact` + `Compact`）
- ✅ Circuit breaker（`failureCount >= maxFailures`，默认 3）
- ⚠️ `microCompact` 方法（仅保留首条 + 后半消息，非常简化）
- ⚠️ `summarize` 方法（LLM 摘要，对应 Tier 3）
- ❌ 缺少 Tier 2 Session Memory Compact

**差距分析**:
- ✅ Circuit breaker 模式已实现
- ✅ LLM 摘要压缩（Tier 3 简化版）已实现
- ⚠️ Micro-compact 过于简化——原始版本按工具类型过滤、保留最近 N 条、支持 cache-editing API
- ❌ 完全缺少 Session Memory Compact（Tier 2）
- ❌ 缺少阈值计算逻辑（effectiveContextWindow 公式）
- ❌ 缺少 `autoCompactIfNeeded()` 的编排逻辑（先尝试 SM，失败后 fallback 到 full）
- ❌ `ShouldCompact` 基于 `m.Tokens` 但 `types.Message` 是否真的有 `Tokens` 字段需确认

**改进建议**:
```go
func (c *LLMCompactor) AutoCompactIfNeeded(ctx context.Context, msgs []types.Message, opts CompactOpts) (*CompactionResult, error) {
    if c.failureCount >= c.maxFailures {
        return nil, nil // circuit breaker open
    }
    // Tier 1: micro-compact (always runs)
    msgs = c.microCompact(msgs, opts.ToolTypes)
    // Check threshold
    if !c.exceedsThreshold(msgs, opts) {
        return nil, nil
    }
    // Tier 2: try session memory compact
    if result, err := c.trySessionMemoryCompact(ctx, msgs, opts); result != nil {
        return result, err
    }
    // Tier 3: full LLM compact
    return c.fullCompact(ctx, msgs)
}
```

---

### 8. Tool 接口完整性

**评级**: ⚠️ 部分覆盖

**原始设计**:
`src/Tool.ts` 定义的 `Tool` 类型包含 **26+ 方法/属性**，按类别分为：
- **Identity & Schema** (7): `name`, `aliases`, `searchHint`, `inputSchema`, `inputJSONSchema`, `outputSchema`, `maxResultSizeChars`
- **Execution** (3): `call()`, `description()`, `prompt()`（文档中提到但在类型定义中通过其他机制实现）
- **Capability Queries** (9): `isEnabled()`, `isReadOnly()`, `isConcurrencySafe()`, `isDestructive()`, `interruptBehavior()`, `isSearchOrReadCommand()`, `isOpenWorld()`, `requiresUserInteraction()`, `inputsEquivalent()`
- **Validation & Permissions** (3): `validateInput()`, `checkPermissions()`, `preparePermissionMatcher()`
- **Path & Input** (3): `getPath()`, `backfillObservableInput()`, `toAutoClassifierInput()`
- **Rendering** (9+): `userFacingName()`, `renderToolUseMessage()`, `renderToolResultMessage()` 等
- 关键属性: `strict`, `shouldDefer`, `alwaysLoad`, `isMcp`, `mcpInfo`

**Go Rebuild 现状**:
`internal/tool/tool.go` 定义了 **5 个方法**：
1. `Name() string`
2. `Description() string`
3. `InputSchema() map[string]any`
4. `Execute(ctx, input) (*ToolResult, error)`
5. `IsReadOnly() bool`

**差距分析**:
对比原始 26+ 方法，Go 版本保留了最核心的 5 个。以下是关键缺失：

| 优先级 | 缺失方法 | 重要性 | 说明 |
|--------|----------|--------|------|
| P0 | `IsEnabled()` | 高 | 工具启用/禁用控制 |
| P0 | `IsConcurrencySafe(input)` | 高 | 并发安全判断（不等于 IsReadOnly） |
| P0 | `ValidateInput(input)` | 高 | 输入验证，在权限检查前执行 |
| P0 | `CheckPermissions(input, ctx)` | 高 | 工具级权限逻辑 |
| P1 | `IsDestructive(input)` | 中 | 不可逆操作标记 |
| P1 | `MaxResultSizeChars` | 中 | 结果大小限制 |
| P1 | `Aliases` | 中 | 向后兼容的别名 |
| P2 | `InterruptBehavior()` | 低 | cancel/block 策略 |
| P2 | Rendering 方法 | 低 | Go 版本可能不需要 React 渲染 |

**改进建议**:
```go
type Tool interface {
    Name() string
    Aliases() []string // P1
    Description() string
    InputSchema() map[string]any
    Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error)

    // Capability queries
    IsEnabled() bool                    // P0
    IsReadOnly() bool
    IsConcurrencySafe(input json.RawMessage) bool  // P0
    IsDestructive(input json.RawMessage) bool       // P1

    // Validation
    ValidateInput(ctx context.Context, input json.RawMessage) error  // P0
    CheckPermissions(ctx context.Context, input json.RawMessage, permCtx *PermissionContext) (PermissionDecision, error) // P0

    // Limits
    MaxResultSize() int // P1, 0 = no limit, -1 = never persist
}
```

---

### 9. ToolUseContext 对照

**评级**: ❌ 缺失

**原始设计**:
`src/Tool.ts` 的 `ToolUseContext` 是一个 **40+ 字段** 的上下文对象，包含：
- **核心选项** (`options`): `commands`, `debug`, `mainLoopModel`, `tools`, `verbose`, `thinkingConfig`, `mcpClients`, `agentDefinitions`, `maxBudgetUsd` 等
- **控制器**: `abortController`, `readFileState`
- **状态访问**: `getAppState()`, `setAppState()`, `setAppStateForTasks()`
- **消息**: `messages: Message[]`
- **代理**: `agentId`, `agentType`
- **UI 回调**: `setToolJSX`, `addNotification`, `appendSystemMessage`, `sendOSNotification`
- **权限**: `toolDecisions`, `localDenialTracking`, `contentReplacementState`
- **追踪**: `queryTracking`, `toolUseId`
- **文件**: `fileReadingLimits`, `globLimits`
- **渲染**: `setInProgressToolUseIDs`, `setResponseLength`, `setStreamMode`

**Go Rebuild 现状**:
**没有 ToolUseContext 或等价类型定义**。工具的 `Execute` 方法只接收 `context.Context` 和 `json.RawMessage`。

**差距分析**:
这是一个重大缺失。ToolUseContext 是工具与引擎之间的关键协议——它让工具能够访问会话状态、读取文件缓存、追踪权限决策、控制 UI 等。没有它，工具只能是纯函数式的输入→输出。

当然，Go 版本不需要 40+ 字段（很多是 React/UI 相关的），但至少需要：
- 会话消息访问
- Abort 信号（已通过 `context.Context` 部分覆盖）
- 权限上下文
- 文件状态缓存
- Agent ID
- 工具列表（工具可能需要知道其他可用工具）

**改进建议**:
```go
type ExecutionContext struct {
    // Session state
    Messages    []types.Message
    SessionID   string
    AgentID     string

    // Permission
    PermissionCtx *PermissionContext

    // File state
    ReadFileCache *FileStateCache

    // Tools
    AvailableTools []Tool

    // Limits
    FileReadLimits *FileReadLimits
    GlobLimits     *GlobLimits

    // Tracking
    QueryTracking *QueryChainTracking
}

// Tool interface 更新
type Tool interface {
    // ...
    Execute(ctx context.Context, input json.RawMessage, execCtx *ExecutionContext) (*types.ToolResult, error)
}
```

---

### 10. 思考模式 (Thinking) 支持

**评级**: ✅ 已覆盖（在架构层面）

**原始设计**:
- `QueryEngine.ts` 支持 `ThinkingConfig` 类型：`{ type: 'adaptive' }` 或 `{ type: 'disabled' }`
- `shouldEnableThinkingByDefault()` 根据模型能力决定默认值
- `query.ts` 中流式循环特别处理 thinking blocks 的规则（注释 :151-163）：
  1. 包含 thinking/redacted_thinking 的消息必须在 `max_thinking_length > 0` 的查询中
  2. thinking block 不能是消息中的最后一个块
  3. thinking blocks 必须在 assistant trajectory 期间保留
- fallback model 切换时会 `stripSignatureBlocks()` 剥离 thinking 签名

**Go Rebuild 现状**:
`ARCHITECTURE.md` 中 Provider 接口的 `ChatRequest` 没有显式提到 thinking，但这可以通过 `ChatRequest` 的扩展字段支持。架构设计层面没有排除 thinking 支持。

**差距分析**:
- ✅ Provider 层的流式设计可以透传 thinking blocks
- ⚠️ 没有显式的 `ThinkingConfig` 类型定义
- ⚠️ 没有 thinking block 的保留/剥离逻辑
- ⚠️ 没有 `stripSignatureBlocks` 在 fallback 时的处理

**改进建议**:
```go
type ThinkingConfig struct {
    Type             string // "adaptive", "disabled", "enabled"
    MaxThinkingLength int   // 0 = disabled
}

// 在 ChatRequest 中加入
type ChatRequest struct {
    // ...
    ThinkingConfig *ThinkingConfig
}
```

---

## 高优先级改进清单

| # | 优先级 | 改进项 | 工作量 | 影响 |
|---|--------|--------|--------|------|
| 1 | **P0** | 实现 queryLoop 五阶段循环 | 大 | 核心引擎 |
| 2 | **P0** | 定义并实现 10 种 Terminal Reasons | 小 | 循环控制 |
| 3 | **P0** | 实现 prompt-too-long 恢复策略 | 中 | 生产可靠性 |
| 4 | **P0** | 实现 max_output_tokens 续行 | 中 | 长输出支持 |
| 5 | **P0** | 定义 ToolUseContext / ExecutionContext | 中 | 工具协议 |
| 6 | **P1** | 扩展 Tool 接口到 10+ 核心方法 | 中 | 工具能力 |
| 7 | **P1** | 实现工具并发执行器 | 中 | 性能 |
| 8 | **P1** | 实现 Session Memory Compact (Tier 2) | 中 | 压缩效率 |
| 9 | **P1** | 实现 fallback model 切换 | 小 | 高可用 |
| 10 | **P2** | 完善 ThinkingConfig 支持 | 小 | 模型能力 |

## 架构风险点

### 1. 核心循环实现空白 — 风险等级: 🔴 高
引擎层目前只有 interface 定义，没有一行循环实现代码。这意味着 Go Rebuild 目前无法执行任何实际的 LLM 对话。所有架构设计、Provider 集成、工具注册都依赖于这个循环的实现。**建议立即开始实现**。

### 2. 错误恢复缺失导致生产不稳定 — 风险等级: 🔴 高
原始代码的三种恢复机制（prompt-too-long、max-output-tokens、fallback）是在生产中经过大量 incident 后沉淀下来的关键路径。没有这些恢复，Go 版本在长对话或高并发场景下会频繁中断。

### 3. ToolUseContext 缺失限制工具能力 — 风险等级: 🟡 中
当前工具只能做纯输入→输出的转换。一旦需要实现 AgentTool（子代理）、FileEditTool（需要文件缓存）或任何需要会话状态的工具，会发现缺少传递上下文的机制。

### 4. 消息类型过于简化 — 风险等级: 🟡 中
原始代码区分 `UserMessage`、`AssistantMessage`、`SystemMessage`、`AttachmentMessage`、`ToolUseSummaryMessage`、`TombstoneMessage` 等 7+ 种消息类型，各有不同的处理逻辑。Go 版本的 `types.Message` 是扁平结构，可能在复杂场景下遇到类型不足的问题。
