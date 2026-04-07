# Go Rebuild 基础设施评审报告

> 评审员: infra-reviewer | 日期: 2026-04-05 | 版本: v1.0

---

## Part A: 接入层与路由层评审（Task #2）

### 1. Channel 接口通用性 ⚠️

**原始设计：**
原始 TypeScript 中 Channel 系统支持三种交互模式：
- **Webhook 回调**（飞书等 IM 平台推送）
- **长连接**（WebSocket 双向实时流）
- **请求-响应**（HTTP REST API 同步请求）

此外，原始代码中 `ChannelEntry` 类型（`bootstrap/state.ts:37-39`）区分了 `plugin` 和 `server` 两种 Channel 类型，支持 marketplace 插件和自定义 MCP server。Bridge 系统（`cli.tsx:112-161`）支持 IDE 扩展的双向通信，使用 JWT 认证。

**现状：**
Go 实现的 `Channel` interface（`internal/channel/channel.go:17-31`）定义了 4 个方法：`Name()`、`Start()`、`Stop()`、`Send()`。接口简洁，基本能覆盖三种模式。

**差距：**
1. **缺少流式回传机制** — `Send()` 只能发送完整 `Message`，但 LLM 响应是流式的（`EngineEvent` channel）。当前 Router 的 `coreHandler`（`router.go:40-64`）只是简单 drain events，**Channel 层完全无法收到流式事件推送给用户**。这是一个严重的架构缺陷。
2. **缺少 Channel 生命周期事件** — 无连接/断开回调，WebSocket Channel 无法通知 Session 层连接状态变化。
3. **缺少健康检查** — 无 `Health()` 或 `IsReady()` 方法，无法在启动时检查 Channel 可用性。
4. **缺少 Bridge/IDE Channel** — 原始代码有完整的 Bridge 系统支持 VS Code/JetBrains，Go 版本无此设计。

**建议：**
```go
type Channel interface {
    Name() string
    Start(ctx context.Context, handler MessageHandler) error
    Stop(ctx context.Context) error
    // SendEvent 替代 Send，支持流式事件
    SendEvent(ctx context.Context, sessionID string, event *types.EngineEvent) error
    // Health 返回 Channel 状态
    Health() HealthStatus
}
```

### 2. IncomingMessage 字段完整性 ⚠️

**原始设计：**
原始 TypeScript 的消息系统非常丰富：
- `Message` 类型（`src/types/message.ts`）包含 `UserMessage`、`AssistantMessage`、`SystemAPIErrorMessage` 等联合类型
- 内容块支持 `text`、`tool_use`、`tool_result`、`image`、`document`、`thinking` 等类型
- 消息包含 `costUSD`、`durationMs`、`uuid`、`cacheStatus` 等元信息
- Bootstrap state 追踪 `totalCostUSD`、`modelUsage`、详细的 token 追踪

**现状：**
Go 的 `IncomingMessage`（`pkg/types/message.go:46-53`）仅 5 个字段：`ChannelName`、`SessionID`、`UserID`、`Text`、`RawPayload`。

**差距：**
1. **缺少多模态内容** — 仅 `Text` 字段，不支持图片、文件、PDF 等内容
2. **缺少消息元信息** — 无 `MessageID`、`Timestamp`、`ReplyTo`（回复引用）
3. **缺少认证上下文** — 无 `AuthToken`、`OrgID`、`SubscriptionType`（原始代码中这些影响重试策略和权限）
4. **ContentBlock 缺少 thinking 类型** — 原始代码支持 extended thinking，Go 版本无此 ContentType
5. **缺少 document 内容类型** — 原始代码有 `BetaRequestDocumentBlock`

**建议：**
```go
type IncomingMessage struct {
    ID          string         // 消息唯一 ID
    ChannelName string
    SessionID   string
    UserID      string
    Content     []ContentBlock // 替代纯 Text，支持多模态
    ReplyTo     string         // 回复引用
    Timestamp   time.Time
    AuthContext  *AuthContext   // 认证信息
    RawPayload  map[string]any
}
```

### 3. 权限系统设计 ❌

**原始设计：**
原始 TypeScript 权限系统（`src/types/permissions.ts`）极其复杂：
- **7 种 PermissionMode**: `default`、`plan`、`acceptEdits`、`bypassPermissions`、`dontAsk`、`auto`（feature-gated）、`bubble`
- **8 种 PermissionRuleSource**: `userSettings`、`projectSettings`、`localSettings`、`flagSettings`、`policySettings`、`cliArg`、`command`、`session`
- **PermissionDecision** 三元组: `allow` | `ask` | `deny`，加 `passthrough`
- **11 种 DecisionReason**: `rule`、`mode`、`subcommandResults`、`hook`、`classifier`、`sandboxOverride`、`safetyCheck` 等
- **ResolveOnce 模式**: 5-way race（用户/hooks/classifier/bridge/channel）
- **ToolPermissionContext**: 只读上下文，包含 rules-by-source、additional working directories

**现状：**
Go 实现完全没有权限系统模块。权限检查流程仅在架构文档（`UML_FLOWS.md` 第 4 节权限检查流程）中描述了简化版本：
- 3 种模式：`bypassPermissions`、`plan`、`default`
- 无 rule 来源区分，无 classifier，无 hook 机制

中间件层的 `auth.go` 只做简单的布尔认证判断，不是工具级权限。

**差距：**
1. **缺少完整的 PermissionMode 枚举** — 缺少 `acceptEdits`、`dontAsk`、`auto`、`bubble`
2. **缺少 PermissionRule 体系** — 无规则来源（8 种 source）、无规则值（toolName + ruleContent）
3. **缺少 PermissionDecision 类型** — 无 `ask` 行为（提示用户确认）
4. **缺少 ToolPermissionContext** — 无法在工具执行前检查权限
5. **缺少 permission hook 机制** — 原始代码支持通过 hooks 自定义权限决策
6. **缺少 classifier 集成** — 原始代码 `auto` 模式使用 LLM classifier 自动判断
7. **这是安全关键路径的严重缺失**

**建议：** 需要新建 `internal/permission/` 包，至少实现：
- `PermissionMode` 枚举（优先实现 5 种外部模式）
- `PermissionRule` 结构（source + behavior + value）
- `PermissionChecker` 接口，在工具执行前调用
- `PermissionDecision` 三元结果类型

### 4. Session 状态管理 ⚠️

**原始设计：**
原始 TypeScript 使用三层状态架构（`docs/03-state-management/README.md`）：
- **Layer 1**: `bootstrap/state.ts` — ~50+ 字段的进程单例，包含 `sessionId`、`modelUsage`、`totalCostUSD`、`cwd`、telemetry counters 等
- **Layer 2**: `AppStateStore.ts` — 响应式 store，包含 settings、permission context、MCP state、plugin state、task state
- **Layer 3**: `AppState.tsx` — React context 桥接（Go 不需要）

Token tracking 非常详细：`totalInputTokens`、`totalOutputTokens`，per-model `ModelUsage`（含 `cacheReadInputTokens`、`cacheCreationInputTokens`）。`totalCostUSD` 实时累加。

**现状：**
Go 的 `Session`（`internal/engine/session/session.go:22-39`）有基本字段：`ID`、`State`、`Messages`、`TotalInputTokens`、`TotalOutputTokens`、`ChannelName`、`UserID`、`Metadata`。

`Manager`（`manager.go`）实现了 `GetOrCreate()`、`Get()`、`PersistAll()`、`CleanupIdle()`。

**差距：**
1. **缺少 LRU 淘汰** — `Manager.active` 是简单 map，无 LRU 机制，高并发时内存不可控
2. **缺少 per-model token tracking** — 只有总量，无法区分不同模型的消耗
3. **缺少 cost tracking** — 无 `totalCostUSD`，无法计算会话成本
4. **缺少 cache token tracking** — 无 `CacheRead`/`CacheWrite` token 统计
5. **缺少会话恢复的消息验证** — `LoadSession` 直接使用存储数据，无 tool-result pairing 验证（原始代码有 `ensureToolResultPairing`）
6. **缺少 Compacting 状态** — Session 状态枚举无 `Compacting` 状态
7. **缺少 idle cleanup 的定时触发** — `CleanupIdle()` 存在但 `main.go` 未启动定时器

**建议：**
- 引入 LRU cache（`hashicorp/golang-lru` 或 `groupcache`）替代简单 map
- 添加 `ModelUsage map[string]*Usage` 字段
- 添加 `TotalCostUSD float64` 字段
- 在 `main.go` 中启动 `time.Ticker` 定期调用 `CleanupIdle()`

### 5. Router 数据流 ⚠️

**原始设计：**
Channel → Router → Middleware → Engine 单向流，Engine 事件通过流式 channel 回传。
原始代码中 Router 不直接存在，而是通过 Commander.js action → init → QueryEngine → tools 的调用链实现。

**现状：**
Go 的 Router（`internal/router/router.go`）实现了 `Handle()` → middleware chain → `coreHandler()` → `engine.ProcessMessage()` 的正向流。

**差距：**
1. **反向数据流断裂** — `coreHandler()` 中 `for range events {}` 只是 drain，没有将 events 回传给 Channel。**用户永远收不到 LLM 响应**。这是最严重的功能性缺陷。
2. **缺少 Channel 引用** — Router 不知道消息来自哪个 Channel，无法路由响应回去
3. **缺少并发安全** — 多 Channel 同时调用 `Handle()` 时，engine 事件如何分发？

**建议：**
Router 需要持有 Channel 注册表，coreHandler 应该将 events 转发给对应 Channel：
```go
func (r *Router) coreHandler(ctx context.Context, msg *types.IncomingMessage) error {
    events, err := r.engine.ProcessMessage(ctx, msg.SessionID, userMsg)
    // 找到来源 channel 并转发事件
    ch := r.channels[msg.ChannelName]
    for evt := range events {
        ch.SendEvent(ctx, msg.SessionID, &evt)
    }
    return nil
}
```

### 6. EventBus Topic 覆盖 ⚠️

**原始设计：**
原始代码中事件系统比较分散，通过 `onChangeAppState` 回调、hooks（`PermissionRequest`、`ToolInput`、`ToolOutput` 等 HookEvent 类型）、以及 signal 机制实现模块解耦。

**现状：**
Go 的 EventBus（`internal/event/bus.go`）定义了 6 个 Topic：
- `session.created`, `session.archived`
- `tool.executed`
- `query.started`, `query.completed`
- `compact.triggered`

**差距：**
1. **缺少连接事件** — `channel.connected`、`channel.disconnected`（WebSocket 场景）
2. **缺少权限事件** — `permission.requested`、`permission.decided`
3. **缺少错误事件** — `api.error`、`api.retry`（原始代码详细追踪重试事件）
4. **缺少 session 状态变更事件** — `session.idle`、`session.restored`、`session.terminated`
5. **缺少模型切换事件** — `provider.switched`、`provider.fallback`
6. **Publish 是同步的** — `Publish()` 同步调用所有 handler，一个慢 handler 会阻塞整个发布链
7. **PublishAsync 无 panic recovery** — `go h(evt)` 无 recover，单个 handler panic 会导致 goroutine 泄露

**建议：**
- 补充上述缺失 Topic
- `PublishAsync` 添加 panic recovery
- 考虑有缓冲的异步发布（buffered channel + worker goroutine）

### 7. 中间件实现质量 ⚠️

**现状分析：**

**auth.go** — 简单但可用。接受一个 `validator` 函数，灵活度高。
- ✅ 函数式注入，易测试
- ⚠️ 缺少认证信息传递 — 验证通过后不将 user/org 信息注入 context
- ⚠️ 缺少 JWT 具体实现 — 只有框架，无实际 JWT 解析逻辑

**ratelimit.go** — 实现了基本的固定窗口限流。
- ✅ per-user 限流
- ❌ **内存泄漏** — `counters` map 只增不减，已过期的 bucket 永不清理
- ⚠️ 固定窗口算法有突发流量问题（窗口边界可以2倍突发）
- ⚠️ 缺少 per-session 限流（原始代码有 session 级别的限制）
- ⚠️ 重启后限流状态丢失（可接受但需注意）

**logging.go** — 简洁实用。
- ✅ 记录 channel、session_id、user_id、duration
- ⚠️ 缺少请求 ID（trace ID）用于链路追踪
- ⚠️ 错误日志和成功日志使用同一级别 `Info`，应该在 err != nil 时用 `Error`

**建议：**
```go
// ratelimit.go 修复内存泄漏
func RateLimit(maxRequests int, window time.Duration) Middleware {
    // 添加过期清理
    go func() {
        ticker := time.NewTicker(window)
        defer ticker.Stop()
        for range ticker.C {
            mu.Lock()
            for k, b := range counters {
                if time.Since(b.windowStart) > window {
                    delete(counters, k)
                }
            }
            mu.Unlock()
        }
    }()
    // ...
}
```

---

## Part B: Provider 层与基础设施评审（Task #3）

### 1. Provider 接口灵活性 ⚠️

**原始设计：**
原始 TypeScript `getAnthropicClient()`（`src/services/api/client.ts:88-316`）支持 4 种后端：
- **Direct (Anthropic)** — OAuth + API Key 认证
- **Bedrock (AWS)** — AWS credentials + Bearer Token + region override
- **Foundry (Azure)** — Azure AD Token + API Key
- **Vertex (GCP)** — GoogleAuth + project ID fallback

通过环境变量动态选择：`CLAUDE_CODE_USE_BEDROCK`、`CLAUDE_CODE_USE_FOUNDRY`、`CLAUDE_CODE_USE_VERTEX`。

Bifrost 在原始代码中**不存在**——原始代码使用 `@anthropic-ai/sdk` 及其子 SDK（bedrock-sdk、vertex-sdk、foundry-sdk）。

**现状：**
Go 的 `Provider` interface（`internal/provider/provider.go:41-50`）定义了 3 个方法：`Chat()`、`CountTokens()`、`Name()`。

`ChatRequest`（L15-23）包含基本字段，`ChatStream`（L33-38）使用 Go channel 传递流式事件。

**差距：**
1. **缺少 Bedrock/Vertex/Foundry 后端** — 架构文档提到 Bifrost 统一接口，但 Bifrost 可能不支持所有后端特性
2. **ChatRequest 缺少关键字段**：
   - 无 `ThinkingConfig`（extended thinking 的 budget tokens）
   - 无 `CacheControl`（prompt caching）
   - 无 `EffortLevel`（模型 effort 参数）
   - 无 `BetaHeaders`（beta 功能开关）
   - 无 `FastMode` 标识
   - 无 `ExtraBody`（自定义参数透传）
   - 无 `FallbackModel`（529 fallback）
3. **缺少客户端构建参数** — 无 `fetchOverride`、`source` tag 等调试信息
4. **缺少 streaming 控制** — 无法在流中间发送取消信号（虽然 ctx 可以取消，但缺少 AbortSignal 等价物的状态管理）

**建议：**
```go
type ChatRequest struct {
    Model          string
    Messages       []types.Message
    System         string
    Tools          []ToolSchema
    MaxTokens      int
    Temperature    float64
    ThinkingConfig *ThinkingConfig // 新增
    CacheControl   *CacheControl   // 新增
    FallbackModel  string          // 新增
    ExtraParams    map[string]any  // 新增
}
```

### 2. 重试策略完整性 ❌

**原始设计：**
`withRetry.ts`（~823 行）实现了极其复杂的重试状态机：
- **Normal Mode**: 10 次重试，500ms * 2^n 指数退避，32s cap，25% jitter
- **529 Handling**: 连续 3 次 529 后 `FallbackTriggeredError` 触发模型降级
- **Fast Mode**: 429/529 短延迟（<20s）保持快速模式，长延迟切换标准模型，cooldown 最少 10 分钟
- **Persistent Retry**: `CLAUDE_CODE_UNATTENDED_RETRY` 模式，5 分钟 max backoff，6 小时 reset cap，30 秒心跳
- **Auth Recovery**: 401 刷新 OAuth，403 token revoked 处理，Bedrock/Vertex/Foundry 各自的认证错误恢复
- **ECONNRESET/EPIPE**: 禁用 keep-alive 后重连
- **Foreground vs Background**: 后台查询 529 直接放弃不重试，防止级联放大
- **x-should-retry header**: 尊重服务端指示，对 ClaudeAI 订阅用户特殊处理
- **max_tokens overflow**: 自动调整输出 token 数

**现状：**
Go 版本完全没有重试实现。`DEPENDENCIES.md` 选用了 `hashicorp/go-retryablehttp`，但：
1. `go-retryablehttp` 是 HTTP 层重试，不了解 LLM API 语义（529、fast mode、fallback）
2. 无代码实现，仅在文档中提及

**差距：**
1. **完全缺失** — 无任何重试逻辑代码
2. `go-retryablehttp` 无法实现：529 fallback、fast mode cooldown、persistent retry heartbeat、foreground/background 区分
3. 缺少 `CannotRetryError`、`FallbackTriggeredError` 错误类型
4. 缺少重试过程中的状态反馈（原始代码 yield `SystemAPIErrorMessage`）

**建议：**
需要自建重试引擎 `internal/provider/retry/`：
- 实现 `AsyncGenerator` 等价的 Go channel 模式
- 支持 529 连续计数 + fallback
- 支持 context cancellation 和心跳
- 可以使用 `go-retryablehttp` 作为底层 HTTP 传输，但重试逻辑必须自建

### 3. 运行时 Provider 切换 ⚠️

**原始设计：**
- 环境变量 `CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY` 控制全局后端
- `mainLoopModelOverride` 支持会话级模型覆盖
- `FallbackTriggeredError` 在运行时从 Opus → Sonnet 降级
- Fast mode 动态切换到标准速度模型

**现状：**
Go 配置中 `llm.default_provider` 是静态的。`Provider` interface 无运行时切换机制。

**差距：**
1. **缺少 Provider 注册表** — 无法根据会话配置选择不同 Provider
2. **缺少 fallback chain** — 当主 Provider 不可用时无降级路径
3. **缺少会话级 model override** — Session 无 `ModelOverride` 字段

**建议：**
添加 `ProviderRegistry`：
```go
type ProviderRegistry struct {
    providers map[string]Provider
    fallbacks map[string]string // model -> fallback model
}

func (r *ProviderRegistry) GetForSession(session *Session) Provider
```

### 4. 配置系统完整性 ⚠️

**原始设计：**
原始 TypeScript 配置来源极其多样：
- 环境变量（`ANTHROPIC_API_KEY` 等数十个）
- CLI 参数（`--model`、`--permission-mode` 等）
- Feature flags（GrowthBook runtime）
- MDM/Policy settings（企业管理）
- Remote managed settings

配置优先级：CLI > env > local > project > user > policy > defaults

**现状：**
Go 的 `Config`（`internal/config/config.go`）使用 Viper，支持 YAML + 环境变量。覆盖了 7 个域：Server、Log、LLM、Engine、Session、Channel、Tools。

**差距：**
1. **缺少多层设置源** — 无 user/project/local 三层 settings.json
2. **缺少 CLAUDE.md 加载** — 原始代码从工作目录加载 CLAUDE.md 作为系统提示
3. **缺少配置验证** — `Load()` 没有使用 validator 验证必填字段
4. **缺少配置热更新** — Viper 支持但未启用 `WatchConfig()`
5. **缺少 API 相关配置**：
   - 无 `api_timeout`（原始默认 600s）
   - 无 `max_retries`（原始默认 10）
   - 无 `proxy` 配置（原始支持 HTTP/HTTPS/SOCKS proxy）
   - 无 `custom_headers`（原始 `ANTHROPIC_CUSTOM_HEADERS`）
   - 无 thinking config（budget tokens）
   - 无 prompt caching 开关
6. **缺少权限模式配置** — 无 `permission_mode` 字段
7. **环境变量前缀不一致** — Go 用 `CLAUDE_`，原始用 `ANTHROPIC_`、`CLAUDE_CODE_`、`AWS_` 等多种前缀

**建议：**
```go
type LLMConfig struct {
    // ... 现有字段
    MaxRetries    int           `mapstructure:"max_retries"`
    APITimeout    time.Duration `mapstructure:"api_timeout"`
    ProxyURL      string        `mapstructure:"proxy_url"`
    CustomHeaders map[string]string `mapstructure:"custom_headers"`
}

type PermissionConfig struct {
    Mode          string            `mapstructure:"mode"` // default, plan, bypassPermissions...
    AllowedTools  []string          `mapstructure:"allowed_tools"`
    DeniedTools   []string          `mapstructure:"denied_tools"`
}
```

### 5. 错误处理体系 ⚠️

**原始设计：**
`src/services/api/errors.ts` (~600 行) 定义了丰富的错误分类：
- `PROMPT_TOO_LONG_ERROR_MESSAGE`
- `CREDIT_BALANCE_TOO_LOW_ERROR_MESSAGE`
- `INVALID_API_KEY_ERROR_MESSAGE`
- `TOKEN_REVOKED_ERROR_MESSAGE`
- `REPEATED_529_ERROR_MESSAGE`
- `API_TIMEOUT_ERROR_MESSAGE`
- `parsePromptTooLongTokenCounts()` — 从错误中提取 token 数
- `isMediaSizeError()` — 检测媒体大小错误
- `getAssistantMessageFromError()` — 转化为用户友好消息

**现状：**
Go 的 `DomainError`（`pkg/errors/errors.go`）定义了 10 个错误码：
`NOT_FOUND`、`PERMISSION_DENIED`、`TIMEOUT`、`RATE_LIMIT`、`INVALID_INPUT`、`PROVIDER_ERROR`、`TOOL_EXEC_ERROR`、`SESSION_NOT_FOUND`、`CONTEXT_OVERFLOW`、`INTERNAL`

**差距：**
1. **缺少 API 层错误码**：
   - `AUTH_FAILED` — API Key/OAuth 认证失败
   - `CREDIT_EXHAUSTED` — 额度不足
   - `OVERLOADED` — 529 过载
   - `TOKEN_REVOKED` — OAuth token 被撤销
   - `MODEL_NOT_AVAILABLE` — 模型不可用
   - `FALLBACK_TRIGGERED` — 触发模型降级
2. **缺少错误到用户消息的转换** — 无 `getAssistantMessageFromError()` 等价物
3. **缺少错误中的结构化数据提取** — 无法从 API 错误中提取 token 数、重试时间等
4. **DomainError 的 `errors.Is` 支持不完整** — 虽然实现了 `Unwrap()`，但没有 sentinel error 变量（如 `var ErrSessionNotFound = New(CodeSessionNotFound, "session not found")`）

**建议：**
添加 sentinel errors 和 API 层错误码：
```go
var (
    ErrSessionNotFound = New(CodeSessionNotFound, "session not found")
    ErrAuthFailed      = New(CodeAuthFailed, "authentication failed")
    ErrOverloaded      = New(CodeOverloaded, "service overloaded")
)
```

### 6. 依赖选型合理性 ✅

**评审 DEPENDENCIES.md：**

| 依赖 | 评价 | 说明 |
|------|------|------|
| Gin | ✅ 合理 | 成熟稳定，适合 HTTP API + middleware |
| gorilla/websocket | ⚠️ 可接受 | 已 archived 后 unarchived，建议关注 nhooyr.io/websocket |
| Viper | ✅ 合理 | 配置管理标准选择 |
| Zap | ✅ 合理 | 高性能结构化日志 |
| Bifrost | ⚠️ 需验证 | 2k Stars，较新项目，需验证其 Anthropic provider 的完整性和流式支持 |
| anthropic-sdk-go | ✅ 合理 | 官方 SDK 作为降级方案 |
| modernc.org/sqlite | ✅ 合理 | 纯 Go，无 CGO |
| go-retryablehttp | ⚠️ 不够 | HTTP 层重试无法满足 LLM API 语义重试需求 |
| testify | ✅ 合理 | 标准测试框架 |
| conc | ✅ 合理 | panic-safe 并发 |

**主要风险：**
1. **Bifrost 成熟度** — 需要实际验证其 streaming、tool calling、thinking 等高级特性的支持程度
2. **缺少 MCP SDK** — DEPENDENCIES.md 提到 `mcp-go`（Mark3Labs），但 go.mod 中未引入
3. **缺少 OpenTelemetry** — 原始代码深度集成了 OpenTelemetry，Go 版本无此依赖

### 7. go.mod 完整性 ❌

**go.mod 审查：**

```
module github.com/anthropics/claude-code-go
go 1.22
```

**问题：**

| 问题 | 严重度 | 说明 |
|------|--------|------|
| Go 版本过低 | ⚠️ | `go 1.22`，DEPENDENCIES.md 指定 `go 1.23.0`，不一致 |
| 缺少 Bifrost | ❌ | DEPENDENCIES.md 列出但 go.mod 未包含 `github.com/maximhq/bifrost` |
| 缺少 anthropic-sdk-go | ❌ | DEPENDENCIES.md 列出但 go.mod 未包含 |
| 缺少 JWT | ❌ | DEPENDENCIES.md 列出但 go.mod 未包含 `github.com/golang-jwt/jwt/v5` |
| 缺少 goldmark | ❌ | DEPENDENCIES.md 列出但 go.mod 未包含 |
| 缺少 conc | ❌ | DEPENDENCIES.md 列出但 go.mod 未包含 `github.com/sourcegraph/conc` |
| 版本不匹配 | ⚠️ | sqlite `v1.29.6` vs DEPENDENCIES.md `v1.34.5`；testify `v1.9.0` vs `v1.10.0`；validator `v10.20.0` vs `v10.23.0`；retryablehttp `v0.7.6` vs `v0.7.7`；sync `v0.7.0` vs `v0.10.0` |
| 缺少 go.sum | ⚠️ | 无 go.sum 文件，无法验证依赖完整性 |

**go.mod 与 DEPENDENCIES.md 偏差汇总：go.mod 有 10 个依赖，DEPENDENCIES.md 指定 14 个，缺少 4 个关键依赖。**

### 8. main.go 启动序列 ⚠️

**原始设计 16 步初始化（docs/01-boot-entrypoint/README.md）：**

| # | 步骤 | 原始实现 |
|---|------|----------|
| 1 | Enable configs | `enableConfigs()` |
| 2 | Safe env vars | `applySafeConfigEnvironmentVariables()` |
| 3 | CA certs | `applyExtraCACertsFromConfig()` |
| 4 | Graceful shutdown | `setupGracefulShutdown()` |
| 5 | 1P event logging | `initialize1PEventLogging()` |
| 6 | OAuth info | `populateOAuthAccountInfoIfNeeded()` |
| 7 | JetBrains detect | `initJetBrainsDetection()` |
| 8 | Repo detect | `detectCurrentRepository()` |
| 9 | Remote settings | `initializeRemoteManagedSettingsLoadingPromise()` |
| 10 | Policy limits | `initializePolicyLimitsLoadingPromise()` |
| 11 | First start time | `recordFirstStartTime()` |
| 12 | mTLS | `configureGlobalMTLS()` |
| 13 | Proxy agents | `configureGlobalAgents()` |
| 14 | API preconnect | `preconnectAnthropicApi()` |
| 15 | Windows shell | `setShellIfWindows()` |
| 16 | Scratchpad | `ensureScratchpadDir()` |

**现状（main.go 11 步）：**

| # | 步骤 | 状态 |
|---|------|------|
| 1 | Load config | ✅ 已实现 |
| 2 | Init logger | ✅ 已实现 |
| 3 | Init storage | ❌ TODO |
| 4 | Create event bus | ✅ 已实现（但未使用） |
| 5 | Register tools | ❌ TODO |
| 6 | Init provider | ❌ TODO |
| 7 | Create session manager | ❌ TODO |
| 8 | Create engine | ❌ TODO |
| 9 | Build router | ❌ TODO |
| 10 | Start channels | ❌ TODO |
| 11 | Wait for shutdown | ✅ 已实现 |

**差距：**
1. **11 步中 7 步是 TODO** — 启动序列骨架存在但大部分未实现
2. **缺少原始的关键步骤**：
   - 无 CA certs / mTLS 配置
   - 无 proxy 配置
   - 无 API preconnect（原始代码在启动时并行建立 TCP+TLS 连接以减少首次请求延迟）
   - 无 graceful shutdown 实现（信号捕获有，但 shutdown 逻辑全是 TODO）
   - 无并行 prefetch（原始代码并行启动 MDM、keychain、GrowthBook）
3. **flag 仅支持 --config** — 原始代码支持 `--model`、`--permission-mode`、`--allowedTools`、`--verbose` 等数十个 CLI 参数
4. **initLogger 有默认值降级** — level 解析失败回落到 Info 级别，但没有日志通知用户配置有误

**建议：**
优先完成 TODO 项，添加：
- storage 初始化（根据 `cfg.Session.Storage` 选择 memory/sqlite）
- provider 初始化（先实现直连 Anthropic）
- 完整的 graceful shutdown 序列
- API preconnect goroutine

---

## 高优先级改进清单

| 优先级 | 问题 | 影响 | 建议 |
|--------|------|------|------|
| **P0** | Router 反向数据流断裂 | 用户无法收到 LLM 响应 | Router 需持有 Channel 注册表，转发 events |
| **P0** | 权限系统完全缺失 | 安全关键路径无保护 | 新建 `internal/permission/` 包 |
| **P0** | 重试策略完全缺失 | API 调用无容错 | 自建重试引擎 `internal/provider/retry/` |
| **P0** | main.go 7/11 步为 TODO | 服务无法实际运行 | 依次实现 storage→provider→session→engine→router→channels |
| **P1** | go.mod 缺少 4 个关键依赖 | 编译不通过 | 同步 go.mod 与 DEPENDENCIES.md |
| **P1** | IncomingMessage 缺少多模态 | 只支持纯文本 | 改为 `[]ContentBlock` |
| **P1** | Session 无 LRU 淘汰 | 高并发内存泄漏 | 引入 LRU cache |
| **P1** | RateLimit 内存泄漏 | 长期运行 OOM | 添加过期 bucket 清理 |
| **P1** | ChatRequest 缺少 thinking/cache | 无法使用高级 API 特性 | 扩展 ChatRequest 字段 |
| **P2** | EventBus Topic 不完整 | 模块间解耦不够 | 补充连接/权限/错误事件 |
| **P2** | 错误码覆盖不全 | 错误处理粒度不够 | 添加 API 层错误码 + sentinel errors |
| **P2** | 配置缺少 proxy/retry/auth | 企业部署受限 | 扩展配置结构 |
| **P2** | Provider 无运行时切换 | 无法 fallback | 添加 ProviderRegistry |
| **P3** | 缺少 OpenTelemetry | 无可观测性 | 引入 `go.opentelemetry.io/otel` |
| **P3** | 缺少 MCP SDK | 无法集成 MCP tools | 引入 `mcp-go` |

---

## 总结

Go Rebuild 的接入层、路由层、Provider 层和基础设施完成了**骨架级设计**——接口定义清晰，分层架构合理，代码风格符合 Go 惯例。但与原始 TypeScript 代码相比，**实现深度严重不足**：

1. **功能性缺陷**（P0）：Router 反向数据流断裂、权限系统缺失、重试策略缺失、启动序列大部分 TODO
2. **完整性缺陷**（P1）：消息类型不完整、Session 无 LRU、go.mod 与文档不同步
3. **健壮性缺陷**（P2）：限流内存泄漏、错误码不足、配置项不全

建议按 P0 → P1 → P2 顺序推进，优先让系统"能跑起来"（P0），然后"跑得对"（P1），最后"跑得好"（P2）。
