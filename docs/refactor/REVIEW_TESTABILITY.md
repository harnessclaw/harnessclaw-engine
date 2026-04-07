# Go Rebuild — 可测试性与质量风险评审报告

> 版本: v1.0.0 | 日期: 2026-04-05 | 评审人: QA Lead

---

## 1. 接口可 Mock 性评审

### 1.1 逐接口评估

| Interface | 文件位置 | 方法数 | 可 Mock? | Mock 难度 | 备注 |
|-----------|----------|--------|----------|-----------|------|
| **Channel** | `internal/channel/channel.go` | 4 | **优秀** | 低 | 纯接口，`Name()` / `Start()` / `Stop()` / `Send()` 均为简单签名。`Start()` 的 `MessageHandler` 回调设计使得测试可通过注入 handler 来验证消息传递。 |
| **Provider** | `internal/provider/provider.go` | 3 | **良好** | 中 | `Chat()` 返回 `*ChatStream`（含 `<-chan StreamEvent` 和 `Err func() error`），mock 时需构造带缓冲 channel 和 error func 的 ChatStream。模式可行但不够简洁。 |
| **Tool** | `internal/tool/tool.go` | 5 | **优秀** | 低 | 纯接口，`Execute()` 输入为 `json.RawMessage` 输出为 `*ToolResult`，非常容易 mock。`IsReadOnly()` 用于并发控制也很好测试。 |
| **Engine** | `internal/engine/engine.go` | 2 | **优秀** | 中 | `ProcessMessage()` 返回 `<-chan EngineEvent`，mock 时需构造 channel。`AbortSession()` 简单。整体可 mock 性好。 |
| **session.Store** | `internal/engine/session/manager.go` | 3 | **优秀** | 低 | `SaveSession` / `LoadSession` / `DeleteSession` 均为简单 CRUD 接口。已有 `memory.Store` 作为内置测试实现。 |
| **Storage** | `internal/storage/storage.go` | 4+1 | **优秀** | 低 | 继承 `session.Store` 并扩展 `ListSessions` + `Close`，同样简单。 |
| **PermissionChecker** | `internal/tool/permission.go` | 1 | **优秀** | 极低 | 单方法接口 `Check()`，已提供 `AllowAllChecker` 作为 bypass 实现。极易 mock。 |
| **Compactor** | `internal/engine/compact/compactor.go` | 2 | **优秀** | 低 | `ShouldCompact()` 和 `Compact()` 签名清晰。测试可轻松构造返回固定结果的 mock。 |
| **Middleware / Handler** | `internal/router/middleware/middleware.go` | 函数类型 | **优秀** | 低 | `Handler` 和 `Middleware` 均为函数类型，测试中可直接传入匿名函数。`Chain()` 的组合模式非常适合单元测试。 |

### 1.2 总体可 Mock 评级: **A（优秀）**

**优点：**
- 所有核心依赖均通过接口定义，遵循了依赖倒置原则
- 接口方法数控制在 1-5 个，符合接口隔离原则
- `session.Store` 接口定义在消费方包中（避免循环依赖），设计正确
- `AllowAllChecker` 提供了 PermissionChecker 的默认 mock
- `memory.Store` 提供了 Storage 的内存实现，可直接用于测试

**改进建议：**
- `Provider.Chat()` 返回的 `ChatStream` 包含 `<-chan StreamEvent`，建议提供 `NewMockChatStream(events []StreamEvent, err error) *ChatStream` 测试辅助函数
- 缺少统一的 `testutil` 包来提供各接口的 mock 工厂
- `EventBus` 没有定义接口（`Bus` 是具体 struct），需要重构为接口才能 mock

---

## 2. 并发安全审查

### 2.1 session/session.go — RWMutex 使用

**代码审查：**

```go
// AddMessage: 写锁 ✓
func (s *Session) AddMessage(msg types.Message) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.Messages = append(s.Messages, msg)
    s.UpdatedAt = time.Now()
    s.TotalInputTokens += msg.Tokens
}

// GetMessages: 读锁 + copy ✓
func (s *Session) GetMessages() []types.Message {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]types.Message, len(s.Messages))
    copy(result, s.Messages)
    return result
}
```

**评估：良好，有 1 个风险点**

- `AddMessage` 使用写锁，`MessageCount` 和 `GetMessages` 使用读锁，正确
- `GetMessages` 返回的是浅拷贝，如果 `Message` 内含引用类型（`Content []ContentBlock`、`Metadata map[string]any`），调用者修改返回值可能影响原始数据

**风险 R1**: `GetMessages()` 的 shallow copy 不安全。`[]ContentBlock` 是 slice of struct，Go 的 copy 会复制 struct 值，但如果 ContentBlock 将来增加指针/map 字段则会出问题。当前安全但未来脆弱。

**风险 R2**: `Session` struct 的字段如 `ID`、`State`、`ChannelName` 等在外部可直接访问（大写字段），绕过锁保护。例如 `manager.go:59` 中 `stored.State = StateActive` 在获取 manager 锁后直接修改 session 字段，未获取 session 自身的锁。

### 2.2 session/manager.go — 嵌套锁与锁粒度

**评估：有 2 个高风险问题**

**风险 R3（高）: `GetOrCreate` 中的嵌套锁**
```go
func (m *Manager) GetOrCreate(...) (*Session, error) {
    m.mu.Lock()         // 获取 manager 写锁
    defer m.mu.Unlock()

    if s, ok := m.active[sessionID]; ok {
        s.mu.Lock()     // 嵌套获取 session 写锁
        s.State = StateActive
        s.UpdatedAt = time.Now()
        s.mu.Unlock()
        return s, nil
    }
    // ...
}
```
当持有 manager 锁时又获取 session 锁（L48-53），如果其他代码路径以相反顺序获取锁（先 session 锁再 manager 锁），则存在**死锁风险**。当前代码中未发现反向获取路径，但这是一个脆弱的设计——任何新代码不小心就可能引入死锁。

**风险 R4（高）: `CleanupIdle` 中的嵌套锁**
```go
func (m *Manager) CleanupIdle(ctx context.Context) int {
    m.mu.Lock()                    // manager 写锁
    defer m.mu.Unlock()

    for id, s := range m.active {
        s.mu.RLock()               // 嵌套 session 读锁
        updatedAt := s.UpdatedAt
        s.mu.RUnlock()

        if updatedAt.Before(threshold) {
            s.mu.Lock()            // 嵌套 session 写锁（升级！）
            s.State = StateArchived
            s.mu.Unlock()

            m.store.SaveSession(ctx, s)  // I/O 操作持有 manager 锁！
```

此处存在三个问题：
1. 持有 manager 写锁时进行 I/O 操作（`SaveSession`），阻塞所有其他 manager 操作
2. 嵌套锁顺序同上（manager → session），需保证全局一致
3. 循环中 session 锁的 RLock → Unlock → Lock 序列存在 TOCTOU 竞态——在释放读锁到获取写锁之间，session 状态可能已变

**风险 R5（中）: `PersistAll` 的快照竞态**
```go
func (m *Manager) PersistAll(ctx context.Context) error {
    m.mu.RLock()
    sessions := make([]*Session, 0, len(m.active))
    for _, s := range m.active {
        sessions = append(sessions, s)  // 拷贝指针，不是值
    }
    m.mu.RUnlock()

    for _, s := range sessions {
        m.store.SaveSession(ctx, s)  // session 可能在保存期间被修改
    }
}
```
快照的是指针而非值，保存期间 session 内容可能已被修改。虽然 `SaveSession` 实现可能通过 session 自身的锁保护读取，但 `memory.Store.SaveSession` 直接存储指针引用（`s.sessions[sess.ID] = sess`），导致存储中的 session 与活跃 session 是**同一个对象**。

### 2.3 tool/registry.go — 读写锁

**评估：正确**

- `Register` 使用写锁
- `Get` 使用读锁
- `All` 使用读锁并返回新切片
- 线程安全，无问题

### 2.4 event/bus.go — PublishAsync 安全性

**评估：有 1 个中风险问题**

**风险 R6（中）: `PublishAsync` 的 goroutine 泄漏**
```go
func (b *Bus) PublishAsync(evt Event) {
    b.mu.RLock()
    handlers := b.handlers[evt.Topic]
    b.mu.RUnlock()

    for _, h := range handlers {
        go h(evt)  // 无法控制 goroutine 生命周期
    }
}
```
- 每个 handler 启动一个无限制的 goroutine，无 recovery 机制
- 如果 handler panic，goroutine 会崩溃但不会被捕获
- 无法在关闭时等待异步 handler 完成
- `handlers` slice 是 `b.handlers[evt.Topic]` 的直接引用（非拷贝），如果在 `go h(evt)` 执行前有并发 `Subscribe` 修改了 handlers，可能读到不一致数据。但由于 Go 的 slice header 是值复制的，实际底层数组引用是安全的——只是如果 `Subscribe` 导致扩容，新 handler 可能被遗漏。

**风险 R7（低）: `Publish` 同步执行中的 panic 传播**
`Publish` 同步调用所有 handler，如果某个 handler panic，后续 handler 不会执行。

### 2.5 middleware/ratelimit.go — 并发计数器

**评估：正确但有设计缺陷**

**风险 R8（低）: 内存泄漏**
```go
counters := make(map[string]*rateBucket)
```
`counters` map 只增不删。对于长时间运行的服务，不同 UserID 的桶会无限增长。需要周期性清理过期桶。

**风险 R9（低）: 窗口滑动精度**
当前实现是固定窗口而非滑动窗口：窗口到期后完全重置。这意味着在两个窗口交界处，用户可以在短时间内发送 2*maxRequests 个请求。

### 2.6 storage/memory/memory.go — map 并发安全

**评估：正确**

- 所有操作都有 RWMutex 保护
- 读操作用 RLock，写操作用 Lock
- `ListSessions` 中调用 `sess.MessageCount()` 会获取 session 的读锁（嵌套锁），但方向一致（store → session），无死锁风险

**风险 R10（中）: `SaveSession` 存储指针引用**
```go
func (s *Store) SaveSession(_ context.Context, sess *session.Session) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.sessions[sess.ID] = sess  // 存储指针，非深拷贝
    return nil
}
```
存储的是原始 session 指针，外部修改会直接影响存储数据。在测试中可能导致意外的副作用。

### 2.7 并发安全总结

| 风险ID | 组件 | 风险等级 | 描述 |
|--------|------|----------|------|
| R1 | Session | 低 | GetMessages 浅拷贝不安全（未来风险） |
| R2 | Session | 中 | 公开字段可绕过锁保护 |
| R3 | Manager | 高 | GetOrCreate 嵌套锁死锁风险 |
| R4 | Manager | 高 | CleanupIdle 持有锁做 I/O + 嵌套锁 + TOCTOU |
| R5 | Manager | 中 | PersistAll 快照指针竞态 |
| R6 | EventBus | 中 | PublishAsync goroutine 无管控 |
| R7 | EventBus | 低 | Publish 中 panic 传播 |
| R8 | RateLimit | 低 | 计数器 map 内存泄漏 |
| R9 | RateLimit | 低 | 固定窗口精度问题 |
| R10 | MemoryStore | 中 | 存储指针引用非深拷贝 |

---

## 3. 边界条件检查清单

| # | 组件 | 边界条件 | 预期行为 | 风险等级 |
|---|------|----------|----------|----------|
| 1 | Session | Messages 为空时调用 GetMessages | 返回空切片（非 nil） | 低 |
| 2 | Session | AddMessage 并发调用 1000 次 | 消息数正好 1000，token 计数正确 | 高 |
| 3 | Session | TotalInputTokens 溢出 int 上限 | 应有防护或使用 int64 | 中 |
| 4 | Manager | GetOrCreate 传入空 sessionID | 自动生成 UUID | 中 |
| 5 | Manager | 并发 GetOrCreate 相同 sessionID | 只创建一个 session | 高 |
| 6 | Manager | CleanupIdle 时 maxIdle 为 0 | 所有 session 被归档 | 中 |
| 7 | Manager | CleanupIdle 时无活跃 session | 返回 0，无副作用 | 低 |
| 8 | Manager | PersistAll 时 store.SaveSession 全部失败 | 返回第一个错误，尝试所有 session | 中 |
| 9 | Compactor | messages 为空 | ShouldCompact 返回 false | 低 |
| 10 | Compactor | messages 恰好 4 条（Compact 最小阈值） | 不压缩，返回原始消息 | 中 |
| 11 | Compactor | messages 恰好 10 条（micro/full 分界） | 进入 full compact 路径 | 中 |
| 12 | Compactor | circuit breaker 恰好在第 3 次失败 | 第 3 次后 ShouldCompact 返回 false | 高 |
| 13 | Compactor | maxTokens 为 0 | threshold 计算应安全处理 | 高 |
| 14 | Compactor | threshold 为 0 或负数 | 永不触发压缩 | 中 |
| 15 | Compactor | LLM 摘要返回空字符串 | 应视为失败或特殊处理 | 高 |
| 16 | Registry | 注册同名工具 | 返回 error | 中 |
| 17 | Registry | Get 不存在的工具 | 返回 nil | 低 |
| 18 | Registry | All 在空 registry 上调用 | 返回空切片 | 低 |
| 19 | RateLimit | maxRequests 为 0 | 所有请求被拒绝 | 中 |
| 20 | RateLimit | window 为 0 | 每个请求都创建新桶 | 中 |
| 21 | Auth | validator 返回 false | 返回 PermissionDenied 错误 | 低 |
| 22 | Router | Engine.ProcessMessage 返回 error | 错误被记录并返回 | 中 |
| 23 | Router | 中间件链为空 | core handler 直接执行 | 低 |
| 24 | ContextBuilder | parts 为空 | 返回空字符串 | 低 |
| 25 | ContextBuilder | 多个 part 相同 Order | 排序稳定性未保证 | 低 |
| 26 | Config | 配置文件不存在 | 使用默认值 + 环境变量 | 中 |
| 27 | Config | 环境变量覆盖嵌套配置 | CLAUDE_SERVER_PORT 覆盖 server.port | 中 |
| 28 | MemoryStore | 删除不存在的 session | 无副作用 | 低 |
| 29 | MemoryStore | ListSessions filter 全为 nil | 返回全部 session | 低 |
| 30 | MemoryStore | ListSessions limit 大于 session 数 | 返回全部 session | 低 |
| 31 | EventBus | Publish 到无订阅者的 topic | 无操作 | 低 |
| 32 | EventBus | Subscribe 后立即 PublishAsync | handler 在 goroutine 中执行 | 中 |
| 33 | Compactor | Compact 的 ctx 被取消 | LLM 调用应中断，返回 error | 高 |
| 34 | Provider | Chat 返回的 stream channel 在消费前关闭 | 消费者 for-range 立即退出 | 中 |
| 35 | IncomingMessage | Text 为空字符串 | 应被处理或拒绝 | 中 |

---

## 4. 优雅关闭测试矩阵

| 场景 | 预期行为 | 当前实现 | 差距 |
|------|----------|----------|------|
| 收到 SIGINT | 停止接收新请求 → 关闭 Channel → 等待 in-flight → 持久化 → 关闭存储 | `main.go:82-96` 仅监听信号并打印日志，**关闭逻辑全部为 TODO** | **严重缺失**: 无任何实际关闭逻辑 |
| 收到 SIGTERM | 同 SIGINT | 同上 | 同上 |
| Channel.Stop 超时 | 应有 grace period，超时后强制关闭 | 未实现 | 缺失 |
| in-flight 查询超时 | 应有 grace period（如 30s），超时后取消 context | 未实现 | 缺失 |
| 持久化失败 | 应记录错误日志但仍继续关闭其他组件 | `PersistAll` 返回第一个错误但继续尝试 ✓ | PersistAll 本身实现合理 |
| 双重信号（第二次 SIGINT） | 应强制退出 | 未实现 | 缺失 |
| goroutine 泄漏检测 | 关闭后无残留 goroutine | `PublishAsync` 启动的 goroutine 无法跟踪 | 缺失 |
| 数据库连接释放 | `Storage.Close()` 被调用 | TODO | 缺失 |
| Logger flush | `logger.Sync()` 被调用 | `main.go:48` 有 `defer logger.Sync()` ✓ | 已实现 |

**关键差距总结**: `main.go` 的 step 3-10 均为 TODO，优雅关闭序列完全未实现。这是最高优先级的实现任务。

---

## 5. 配置验证缺失项

当前 `config.Load()` 没有任何验证逻辑。以下配置值应在加载后验证：

| # | 配置路径 | 验证规则 | 影响 |
|---|----------|----------|------|
| 1 | `server.port` | 1-65535 范围 | 端口绑定失败 |
| 2 | `server.host` | 合法 IP 或 hostname | 监听失败 |
| 3 | `log.level` | 必须是 debug/info/warn/error 之一 | Zap 静默降级到 info |
| 4 | `log.format` | 必须是 json/console 之一 | 未知格式走 production config |
| 5 | `log.output` | file 时 file_path 必须非空 | 日志丢失 |
| 6 | `llm.default_provider` | 必须在 providers map 中存在 | 运行时 panic |
| 7 | `llm.providers.*.api_key` | 非空 | API 调用失败 |
| 8 | `llm.providers.*.model` | 非空 | API 调用失败 |
| 9 | `llm.providers.*.max_tokens` | > 0 | 无效请求 |
| 10 | `llm.providers.*.temperature` | 0.0-2.0 范围 | API 拒绝 |
| 11 | `engine.max_turns` | > 0 | 无限循环或零轮次 |
| 12 | `engine.auto_compact_threshold` | 0.0-1.0 范围 | 永不/永远压缩 |
| 13 | `engine.tool_timeout` | > 0 | 立即超时 |
| 14 | `session.max_messages` | > 0 | 无消息或无限消息 |
| 15 | `session.idle_timeout` | > 0 | 立即归档 |
| 16 | `session.storage` | memory/sqlite | 未知存储类型 |
| 17 | `channels.feishu.app_id` | enabled=true 时非空 | 飞书初始化失败 |
| 18 | `channels.feishu.app_secret` | enabled=true 时非空 | 飞书初始化失败 |
| 19 | `channels.websocket.path` | 以 / 开头 | 路由注册失败 |
| 20 | `channels.http.path` | 以 / 开头 | 路由注册失败 |
| 21 | `tools.*.timeout` | enabled=true 时 > 0 | 工具立即超时 |
| 22 | 至少一个 channel enabled | 否则服务无用 | 空转 |

**建议**: 在 `config.go` 中增加 `func (c *Config) Validate() error` 方法，在 `main.go` 加载配置后调用。

---

## 6. 关键测试用例清单

| # | 模块 | 测试名称 | 类型 | 优先级 |
|---|------|----------|------|--------|
| 1 | Session | TestAddMessage_ConcurrentSafety | unit | P0 |
| 2 | Session | TestGetMessages_ReturnsCopy | unit | P0 |
| 3 | Session | TestAddMessage_UpdatesTokenCount | unit | P1 |
| 4 | Session | TestMessageCount_EmptySession | unit | P2 |
| 5 | Manager | TestGetOrCreate_NewSession | unit | P0 |
| 6 | Manager | TestGetOrCreate_ExistingActive | unit | P0 |
| 7 | Manager | TestGetOrCreate_RestoreFromStorage | unit | P0 |
| 8 | Manager | TestGetOrCreate_EmptySessionID_GeneratesUUID | unit | P1 |
| 9 | Manager | TestGetOrCreate_ConcurrentSameID | unit | P0 |
| 10 | Manager | TestCleanupIdle_ArchivesExpired | unit | P0 |
| 11 | Manager | TestCleanupIdle_KeepsRecent | unit | P1 |
| 12 | Manager | TestCleanupIdle_StorageFailure_ContinuesLoop | unit | P1 |
| 13 | Manager | TestPersistAll_AllSucceed | unit | P1 |
| 14 | Manager | TestPersistAll_PartialFailure_ReturnsFirstError | unit | P1 |
| 15 | Compactor | TestShouldCompact_BelowThreshold | unit | P0 |
| 16 | Compactor | TestShouldCompact_AboveThreshold | unit | P0 |
| 17 | Compactor | TestShouldCompact_CircuitBreakerOpen | unit | P0 |
| 18 | Compactor | TestCompact_TooFewMessages | unit | P1 |
| 19 | Compactor | TestCompact_MicroCompact | unit | P0 |
| 20 | Compactor | TestCompact_FullCompact_Success | unit | P0 |
| 21 | Compactor | TestCompact_FullCompact_LLMFailure_IncrementsBreaker | unit | P0 |
| 22 | Compactor | TestCompact_FullCompact_Success_ResetsBreaker | unit | P1 |
| 23 | Compactor | TestCompact_ContextCancelled | unit | P1 |
| 24 | Registry | TestRegister_DuplicateName_ReturnsError | unit | P0 |
| 25 | Registry | TestGet_ExistingTool | unit | P1 |
| 26 | Registry | TestGet_NonExistent_ReturnsNil | unit | P1 |
| 27 | Registry | TestAll_SortedByName | unit | P1 |
| 28 | Registry | TestSchemas_MatchesRegistered | unit | P2 |
| 29 | Registry | TestRegister_ConcurrentAccess | unit | P0 |
| 30 | Permission | TestAllowAllChecker_AlwaysAllows | unit | P2 |
| 31 | Middleware | TestChain_OrderPreserved | unit | P0 |
| 32 | Middleware | TestAuth_ValidMessage_Passes | unit | P0 |
| 33 | Middleware | TestAuth_InvalidMessage_ReturnsError | unit | P0 |
| 34 | Middleware | TestRateLimit_WithinLimit_Passes | unit | P0 |
| 35 | Middleware | TestRateLimit_ExceedsLimit_ReturnsError | unit | P0 |
| 36 | Middleware | TestRateLimit_WindowExpiry_Resets | unit | P1 |
| 37 | Middleware | TestRateLimit_DifferentUsers_Independent | unit | P1 |
| 38 | Middleware | TestLogging_RecordsDuration | unit | P2 |
| 39 | Router | TestHandle_DispatchesToEngine | integration | P0 |
| 40 | Router | TestHandle_MiddlewareChainExecuted | integration | P0 |
| 41 | Router | TestHandle_EngineError_PropagatesError | integration | P1 |
| 42 | Router | TestHandle_AuthFailure_StopsChain | integration | P0 |
| 43 | ContextBuilder | TestBuildSystemPrompt_SortsByOrder | unit | P1 |
| 44 | ContextBuilder | TestBuildSystemPrompt_SkipsEmptyContent | unit | P1 |
| 45 | ContextBuilder | TestBuildSystemPrompt_EmptyParts | unit | P2 |
| 46 | EventBus | TestSubscribe_PublishSync_HandlersCalledInOrder | unit | P1 |
| 47 | EventBus | TestPublishAsync_HandlersCalledConcurrently | unit | P1 |
| 48 | EventBus | TestPublish_NoSubscribers_NoEffect | unit | P2 |
| 49 | MemoryStore | TestSaveLoad_Roundtrip | unit | P0 |
| 50 | MemoryStore | TestDelete_Removes | unit | P1 |
| 51 | MemoryStore | TestListSessions_FilterByState | unit | P1 |
| 52 | MemoryStore | TestListSessions_LimitApplied | unit | P2 |
| 53 | MemoryStore | TestConcurrentAccess | unit | P0 |
| 54 | Config | TestLoad_DefaultValues | unit | P0 |
| 55 | Config | TestLoad_EnvOverride | unit | P0 |
| 56 | Config | TestLoad_FileNotFound_UsesDefaults | unit | P1 |
| 57 | Config | TestLoad_InvalidYAML_ReturnsError | unit | P1 |
| 58 | Errors | TestDomainError_ErrorFormat | unit | P2 |
| 59 | Errors | TestDomainError_Unwrap | unit | P2 |
| 60 | E2E | TestFullPipeline_Channel_Router_Engine_Provider | e2e | P0 |

---

## 7. 集成测试策略建议

### 7.1 端到端测试方案

验证完整链路: **Channel → Router → Middleware → Engine → Provider → Tool → Response**

```
                   ┌──────────────────────────────────────────────────────┐
                   │                  Integration Test                     │
                   │                                                      │
                   │  MockChannel ──→ Router ──→ Engine(real) ──→ MockProvider
                   │       ↑                          │                    │
                   │       │                          ↓                    │
                   │       │                    MockTool                   │
                   │       │                          │                    │
                   │       └──────── response ←───────┘                    │
                   └──────────────────────────────────────────────────────┘
```

### 7.2 测试 Fixture 设计

```go
// testutil/fixtures.go

// NewTestConfig 返回适合测试的最小配置
func NewTestConfig() *config.Config {
    return &config.Config{
        Server:  config.ServerConfig{Host: "127.0.0.1", Port: 0}, // 随机端口
        Log:     config.LogConfig{Level: "debug", Format: "console", Output: "stdout"},
        Engine:  config.EngineConfig{MaxTurns: 5, AutoCompactThreshold: 0.8, ToolTimeout: 5 * time.Second},
        Session: config.SessionConfig{MaxMessages: 100, IdleTimeout: 5 * time.Minute, Storage: "memory"},
    }
}

// NewTestSession 返回一个预填充消息的测试 session
func NewTestSession(id string, messageCount int) *session.Session { ... }

// NewTestIncomingMessage 返回一个标准化的测试消息
func NewTestIncomingMessage(text string) *types.IncomingMessage { ... }
```

### 7.3 Mock Provider 实现方案

```go
// testutil/mock_provider.go

type MockProvider struct {
    ChatFunc       func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error)
    CountTokensFunc func(ctx context.Context, messages []types.Message) (int, error)
}

func (m *MockProvider) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
    return m.ChatFunc(ctx, req)
}

// NewEchoProvider 返回一个回显用户输入的 mock provider
func NewEchoProvider() *MockProvider { ... }

// NewToolCallProvider 返回一个先调用工具再返回结果的 mock provider
// 第一次调用返回 tool_use 事件，第二次调用返回 text + end_turn
func NewToolCallProvider(toolName string, toolInput string) *MockProvider { ... }

// NewStreamFromEvents 从事件列表构造 ChatStream
func NewStreamFromEvents(events []types.StreamEvent, err error) *provider.ChatStream {
    ch := make(chan types.StreamEvent, len(events))
    for _, e := range events {
        ch <- e
    }
    close(ch)
    return &provider.ChatStream{
        Events: ch,
        Err:    func() error { return err },
    }
}
```

### 7.4 测试数据管理

- **测试数据目录**: `testdata/` 放置固定的测试 YAML 配置、消息序列 JSON
- **Golden files**: 对于 ContextBuilder 输出，使用 golden file 模式 (`testdata/golden/system_prompt_*.txt`)
- **表驱动测试**: 所有边界条件测试使用 Go 的 table-driven test 模式
- **随机化**: 并发测试使用 `testing.T.Parallel()` + `-race` flag

### 7.5 CI/CD 集成建议

```yaml
# .github/workflows/test.yml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Unit Tests
        run: go test ./... -race -cover -count=1

      - name: Integration Tests
        run: go test ./... -tags=integration -race -timeout=5m

      - name: Coverage Report
        run: |
          go test ./... -coverprofile=coverage.out
          go tool cover -func=coverage.out

      - name: Lint
        run: golangci-lint run ./...

      - name: Vet
        run: go vet ./...
```

**覆盖率目标:**
- 核心引擎层 (engine, session, compact): >= 80%
- 工具层 (tool, registry, permission): >= 90%
- 中间件 (middleware): >= 90%
- 基础设施 (config, storage, event): >= 85%
- 总体: >= 75%

---

## 8. 总体风险评估

### 整体质量风险等级: **中高**

虽然接口设计优秀（可 mock 性 A 级），但实现层面存在多处并发安全隐患，且核心启动/关闭逻辑和 Engine 实现均未完成。

### Top 5 需要优先解决的测试盲区

| 优先级 | 盲区 | 风险描述 | 建议 |
|--------|------|----------|------|
| **P0** | **Engine 实现缺失** | `engine.go` 仅定义接口，核心查询循环（LLM 调用 → 工具执行 → 续行判断）完全未实现。这是整个系统的心脏，无法测试最关键的业务逻辑。 | 立即实现 `QueryEngine`，包含 5 阶段循环、工具分发、续行逻辑 |
| **P0** | **优雅关闭未实现** | `main.go` 的 step 3-10 全部为 TODO。Channel 停止、in-flight 查询取消、session 持久化、存储关闭均无代码。生产环境部署会导致数据丢失。 | 实现完整的 shutdown 序列，并编写关闭超时、双信号、并发关闭的集成测试 |
| **P0** | **Session Manager 并发安全** | `CleanupIdle` 和 `GetOrCreate` 中的嵌套锁模式（R3, R4）存在死锁风险和 I/O 阻塞问题。这些在低并发时不会暴露，但在负载下会产生严重问题。 | 重构为"先收集再操作"模式：收集阶段只持 RLock，操作阶段逐个加锁。添加 `go test -race` 并发测试。 |
| **P1** | **Compactor failureCount 非线程安全** | `LLMCompactor.failureCount` 和 `maxFailures` 在 `ShouldCompact` 和 `Compact` 中读写，但没有任何锁保护。如果多个 goroutine 同时触发压缩，circuit breaker 计数器会产生竞态。 | 使用 `atomic.Int32` 替代 `int`，或添加 mutex。 |
| **P1** | **配置无验证** | `Config.Load()` 不验证任何值。无效配置（port=0、空 API key、负数 threshold）不会在启动时失败，而是在运行时以难以诊断的方式失败。 | 添加 `Config.Validate()` 方法并在启动时调用。 |

### 次要风险（P2）

- **EventBus 缺少接口**: `Bus` 是具体类型，无法在测试中替换。建议抽取 `EventBus` 接口。
- **memory.Store 存储指针**: 导致存储数据与活跃 session 耦合，测试中容易产生意外副作用。
- **ContextBuilder 排序**: 使用冒泡排序（O(n^2)），且不稳定。建议使用 `sort.SliceStable`。
- **缺少 testutil 包**: 没有统一的 mock 工厂、fixture 生成器和 assertion helper，会导致测试代码重复。
- **Router coreHandler 丢弃事件**: `for range events {}` 只是排空 channel，未验证事件内容，也未将事件传递给 Channel 层。

---

## Revision History

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-04-05 | v1.0.0 | 初始可测试性评审报告 |
