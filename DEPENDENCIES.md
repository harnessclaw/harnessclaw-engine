# Go Rebuild — 开源依赖选型文档

> 版本: v1.0.0 | 日期: 2026-04-05 | 作者: 架构组

本文档为 Go Rebuild 项目的依赖选型决策记录。所有选型基于架构设计文档（`ARCHITECTURE.md`）中定义的分层架构：Channel → Router → Engine → Provider/Tool，以及 Infra 层（Config / Logger / Storage / EventBus）的需求。

---

## 1. 依赖选型总览表

| 功能域 | 选定库 | 版本 | 替代方案 | 选定理由 |
|--------|--------|------|----------|----------|
| HTTP 框架 | `github.com/gin-gonic/gin` | v1.10.x | Chi, Echo, net/http | 生态最成熟，中间件丰富，性能优秀 |
| WebSocket | `github.com/gorilla/websocket` | v1.5.x | nhooyr.io/websocket, gobwas/ws | 事实标准，文档充足，社区示例最多 |
| 配置管理 | `github.com/spf13/viper` | v1.19.x | koanf, envconfig | YAML/ENV/热更新一体化，Go 生态首选 |
| 结构化日志 | `go.uber.org/zap` | v1.27.x | zerolog, slog | 极致性能，结构化输出，分级日志 |
| LLM 多供应商 | `github.com/maximhq/bifrost` | v0.2.x | go-openai, anthropic-go | 统一多 Provider 接口，减少适配代码 |
| LLM 直连(备用) | `github.com/anthropics/anthropic-sdk-go` | v0.2.x | 手写 HTTP 客户端 | 官方 SDK，类型安全 |
| 数据库 | `modernc.org/sqlite` | v1.34.x | mattn/go-sqlite3 (CGO) | 纯 Go 实现，无 CGO，交叉编译友好 |
| Schema 验证 | `github.com/go-playground/validator/v10` | v10.23.x | ozzo-validation | struct tag 驱动，社区最广泛 |
| 测试增强 | `github.com/stretchr/testify` | v1.10.x | goconvey, gomega | 断言 + Mock，Go 测试事实标准 |
| 事件总线 | 标准库 `chan` + `sync` | — | EventBus 库 | 内部解耦足够，无需外部依赖 |
| 进程管理 | 标准库 `os/exec` | — | cobra (CLI) | BashTool 进程隔离，stdlib 足够 |
| 错误处理 | 标准库 `fmt.Errorf` + `errors` | — | pkg/errors, cockroachdb/errors | Go 1.21+ 原生 wrap/unwrap 已足够 |
| HTTP 客户端 | `github.com/hashicorp/go-retryablehttp` | v0.7.x | resty, 标准 net/http | 内建重试/退避，适合 LLM API 调用 |
| UUID | `github.com/google/uuid` | v1.6.x | — | 标准选择，无争议 |
| JWT | `github.com/golang-jwt/jwt/v5` | v5.2.x | — | 社区标准，活跃维护 |
| Markdown 处理 | `github.com/yuin/goldmark` | v1.7.x | blackfriday/v2 | 可扩展性最好，CommonMark 合规 |
| 并发工具 | `golang.org/x/sync` | v0.10.x | sourcegraph/conc | errgroup/singleflight 覆盖核心场景 |
| 结构化并发 | `github.com/sourcegraph/conc` | v0.3.x | — | 补充 panic 安全、pool 等高级模式 |

---

## 2. 逐域详细分析

### 2.1 HTTP 框架

HTTP 框架承载三个关键职责：HTTP API Channel 的路由处理、WebSocket 升级端点、以及全局中间件链（认证/限流/日志）。

#### 候选库对比

| 维度 | Gin | Chi | Echo | net/http (stdlib) |
|------|-----|-----|------|-------------------|
| GitHub Stars | 80k+ | 18k+ | 30k+ | — |
| 维护状态 | 活跃 | 活跃 | 活跃 | 随 Go 版本更新 |
| 最低 Go 版本 | 1.22 | 1.22 | 1.22 | — |
| 路由性能 | 极快 (httprouter) | 快 (radix trie) | 快 | 一般 |
| 中间件生态 | 丰富 (gin-contrib) | 中等 | 丰富 | 需手写 |
| JSON 绑定/验证 | 内建 | 无 | 内建 | 需手写 |
| Context 集成 | 自有 gin.Context | 标准 context | 自有 echo.Context | 标准 context |
| 学习曲线 | 低 | 低 | 低 | 低 |

#### 选定：Gin (`github.com/gin-gonic/gin`)

**选定理由：**
1. **生态最成熟** — 80k+ Stars，Go Web 框架中使用最广泛，遇到问题时 StackOverflow/GitHub 资源最多
2. **中间件丰富** — `gin-contrib` 提供 CORS、限流、JWT、Prometheus 等开箱即用中间件，与架构文档中的 Middleware Chain 完美匹配
3. **内建绑定与验证** — 自带 JSON/YAML/Form 绑定及 `validator` 集成，减少 HTTP API Channel 的样板代码
4. **性能优异** — 基于 httprouter 的路由匹配，在高并发场景下表现稳定

**使用场景：**
- `internal/channel/http/` — HTTP API Channel 的路由与请求处理
- `internal/channel/websocket/` — WebSocket 升级端点 (`GET /ws`)
- `internal/router/middleware/` — 认证、限流、请求日志等中间件
- `cmd/server/main.go` — HTTP 服务器启动与优雅关闭

**备注：** Chi 基于标准 `net/http` 生态，如未来需要更贴近标准库风格可考虑迁移，二者 API 差异不大。

---

### 2.2 WebSocket

WebSocket Channel 需要支持长连接、双向通信、心跳检测，用于客户端与引擎的实时流式交互。

#### 候选库对比

| 维度 | gorilla/websocket | nhooyr.io/websocket | gobwas/ws |
|------|-------------------|---------------------|-----------|
| GitHub Stars | 22k+ | 3.5k+ | 6k+ |
| 维护状态 | 社区维护 (archived → unarchived) | 活跃 | 活跃 |
| 最低 Go 版本 | 1.20 | 1.21 | 1.18 |
| API 风格 | 底层控制 | 基于 context | 零分配底层 |
| 并发写支持 | 需手动加锁 | 内建 | 需手动处理 |
| 压缩支持 | permessage-deflate | permessage-deflate | permessage-deflate |
| 社区示例 | 最多 | 中等 | 较少 |
| io.ReadWriter 兼容 | 否 | 是 | 是 |

#### 选定：gorilla/websocket (`github.com/gorilla/websocket`)

**选定理由：**
1. **事实标准** — Go WebSocket 领域使用最广泛的库，几乎所有教程和项目都基于它
2. **文档与示例** — 官方示例覆盖聊天室、echo 等典型场景，与我们的 WebSocket Channel 需求高度匹配
3. **稳定可靠** — 经过大量生产验证，已知问题少
4. **与 Gin 无缝集成** — 通过 `websocket.Upgrader` 即可在 Gin handler 中升级连接

**使用场景：**
- `internal/channel/websocket/` — WebSocket Channel 核心实现
- 客户端连接管理、心跳 ping/pong、消息读写循环
- 流式响应推送（Engine 产出的 `EngineEvent` 实时发送到客户端）

**注意事项：** gorilla/websocket 不支持并发写，我们需要在 WebSocket Channel 实现中加写锁或使用单独的写 goroutine + channel 模式。

---

### 2.3 配置管理

架构文档明确要求"配置驱动"，配置结构涵盖 server、log、llm、engine、session、channels、tools 七大域。

#### 候选库对比

| 维度 | Viper | koanf | envconfig |
|------|-------|-------|-----------|
| GitHub Stars | 27k+ | 2.8k+ | 5k+ |
| 维护状态 | 活跃 (v1.19+) | 活跃 | 维护模式 |
| 多格式支持 | YAML/JSON/TOML/ENV/etcd/consul | YAML/JSON/TOML/ENV | 仅 ENV |
| 环境变量覆盖 | 自动绑定 | 手动配置 | 原生 |
| 热更新 (Watch) | 支持 (fsnotify) | 支持 | 不支持 |
| 嵌套结构 | 支持 | 支持 | 有限 |
| 远程配置源 | etcd/consul | 无 | 无 |

#### 选定：Viper (`github.com/spf13/viper`)

**选定理由：**
1. **架构指定** — `ARCHITECTURE.md` 中 Infra 层明确标注 `Config (Viper)`
2. **多源融合** — 支持 YAML 配置文件 + 环境变量覆盖 + 默认值，完美匹配 `configs/config.yaml` 中的 `${ANTHROPIC_API_KEY}` 等环境变量引用
3. **热更新** — 基于 fsnotify 的配置文件监听，支持运行时调整日志级别、LLM 参数等
4. **生态标配** — Go 项目配置管理的第一选择，与 Cobra 等工具链天然集成

**使用场景：**
- `internal/config/config.go` — 加载 `configs/config.yaml`，绑定环境变量
- 全局配置注入 — 各层通过依赖注入获取配置，非全局变量
- 敏感信息 — API Key 等通过 `${ENV_VAR}` 从环境变量注入，不入库

---

### 2.4 结构化日志

日志贯穿所有层级，架构文档中 QueryEngine、Router、ToolRegistry 均依赖 Logger。需要结构化输出（JSON/Console）、分级控制、高性能。

#### 候选库对比

| 维度 | Zap | zerolog | slog (stdlib) |
|------|-----|---------|---------------|
| GitHub Stars | 22k+ | 10k+ | — |
| 维护状态 | 活跃 (Uber) | 活跃 | Go 1.21+ 内建 |
| 零分配 | SugaredLogger 少量分配 | 完全零分配 | 少量分配 |
| 性能 (ns/op) | ~100ns | ~80ns | ~200ns |
| 结构化输出 | JSON/Console | JSON/Console | JSON/Text |
| 采样支持 | 内建 | 无 | 无 |
| Hook 机制 | 支持 | 支持 | 支持 (Handler) |
| 上下文集成 | 需手动 | 原生 context | 原生 context |

#### 选定：Zap (`go.uber.org/zap`)

**选定理由：**
1. **架构指定** — `ARCHITECTURE.md` 中明确标注 `Logger (Zap)`
2. **性能极致** — 在高并发工具执行场景下，日志不能成为瓶颈，Zap 的零/少分配设计保证了性能
3. **双模式 Logger** — `Logger`（类型安全、零分配）用于核心路径，`SugaredLogger`（printf 风格）用于开发调试
4. **采样支持** — 生产环境可对高频日志（如每次 StreamEvent）做采样，避免日志风暴
5. **生态集成** — 大量 Go 库支持 Zap Logger 注入

**使用场景：**
- `internal/config/config.go` — 根据 `log.level` / `log.format` 初始化 Logger
- `internal/engine/engine.go` — 记录 LLM 调用、工具执行、token 使用
- `internal/router/middleware/logging.go` — 请求日志中间件
- `internal/tool/*/` — 工具执行日志

**注意事项：** Go 1.21+ 的 `log/slog` 已可用且在标准库中，如未来希望减少外部依赖可考虑迁移。但目前 slog 在性能和功能上仍弱于 Zap。

---

### 2.5 LLM 多供应商

架构文档 Provider 层定义了统一的 `Provider` interface，需要对接 Anthropic、OpenAI 及未来更多 LLM 供应商。

#### 候选库对比

| 维度 | Bifrost SDK | go-openai | anthropic-sdk-go |
|------|-------------|-----------|------------------|
| 定位 | 多 Provider 统一网关 | OpenAI 专用 SDK | Anthropic 官方 SDK |
| GitHub Stars | 2k+ | 9k+ | 1.5k+ |
| 支持 Provider | OpenAI/Anthropic/Bedrock/Vertex 等 | 仅 OpenAI | 仅 Anthropic |
| 流式支持 | 支持 | 支持 | 支持 |
| Tool Calling | 统一格式 | OpenAI 格式 | Anthropic 格式 |
| 连接池/重试 | 内建 | 无 | 基本 |
| 维护状态 | 活跃 | 活跃 | 活跃 (官方) |

#### 选定：Bifrost SDK (`github.com/maximhq/bifrost`) + anthropic-sdk-go (备用)

**选定理由：**
1. **架构指定** — Provider 层设计即围绕 Bifrost 的多 Provider 统一接口
2. **减少适配代码** — 一套 API 对接多个 LLM Provider，避免为每个 Provider 写独立 adapter
3. **内建优化** — 连接池管理、请求重试、负载均衡等功能开箱即用
4. **降级方案** — 保留 `anthropic-sdk-go` 作为直连备用，当 Bifrost 不可用时可快速切换

**使用场景：**
- `internal/provider/bifrost/adapter.go` — Bifrost 适配器，实现 `Provider` interface
- `internal/provider/anthropic/` — Anthropic 直连适配器（降级）
- `internal/provider/openai/` — OpenAI 直连适配器（降级）
- `internal/engine/compact/compactor.go` — 上下文压缩时调用 LLM 生成摘要

---

### 2.6 数据库/存储

架构文档 Storage 层支持内存和 SQLite 两种实现，用于会话持久化、历史记录存储。

#### 候选库对比

| 维度 | modernc.org/sqlite | mattn/go-sqlite3 | GORM | sqlx |
|------|-------------------|-------------------|------|------|
| 实现方式 | 纯 Go（C→Go 转译） | CGO 绑定 | ORM 框架 | SQL 扩展 |
| CGO 依赖 | 无 | 必须 | 取决于 driver | 取决于 driver |
| 交叉编译 | 简单 | 复杂（需交叉 C 编译器） | — | — |
| 性能 | 略低于 CGO 版 (~10-20%) | 原生性能 | 有 ORM 开销 | 接近原生 |
| Stars | 5k+ | 8k+ | 37k+ | 16k+ |
| 部署大小 | 约 +15MB | 约 +5MB | — | — |

#### 选定：modernc.org/sqlite (驱动) + 原生 database/sql (接口)

**选定理由：**
1. **无 CGO 依赖** — 交叉编译无障碍，Docker 镜像可使用 scratch/distroless，部署更简单
2. **零外部依赖** — 不需要系统安装 SQLite 动态库，减少运维负担
3. **性能可接受** — 会话存储的读写频率远低于数据库密集型应用，10-20% 的性能差距无感知
4. **标准接口** — 通过 `database/sql` 标准接口驱动，未来切换 driver 零成本

**不选用 ORM 的理由：**
- 会话存储模型简单（Session / Message 两张表），ORM 带来不必要的复杂度
- `database/sql` + 手写 SQL 更可控、可调试
- 如未来模型变复杂，可按需引入 `sqlx` 做参数绑定和结构体扫描

**使用场景：**
- `internal/storage/sqlite/` — SQLite Storage 实现
- 表结构：`sessions`（会话元信息）、`messages`（消息历史）
- DDL 管理：使用 `PRAGMA user_version` 做简单版本控制，或内嵌 SQL migration

---

### 2.7 Schema 验证

工具输入（`json.RawMessage`）、配置结构、API 请求体均需要验证。

#### 候选库对比

| 维度 | go-playground/validator | ozzo-validation | 手写验证 |
|------|------------------------|-----------------|----------|
| Stars | 17k+ | 3.7k+ | — |
| 验证方式 | Struct Tag | 函数式 | 自定义 |
| 自定义规则 | 支持 | 支持 | 完全自由 |
| 错误信息 | 可定制 | 可定制 | 完全自由 |
| 嵌套验证 | 支持 | 支持 | 需手写 |
| 与 Gin 集成 | 内建 | 需手动 | 需手动 |
| i18n 错误 | 通过翻译器 | 手动 | 手动 |

#### 选定：go-playground/validator (`github.com/go-playground/validator/v10`)

**选定理由：**
1. **Gin 内建集成** — Gin 的请求绑定默认使用 validator，声明 struct tag 即可自动验证，零额外代码
2. **社区最广泛** — 17k+ Stars，遇到任何验证需求几乎都有现成方案
3. **Struct Tag 驱动** — 声明式验证，代码简洁，验证规则与数据结构定义在一起，易于维护

**使用场景：**
- `internal/channel/http/` — HTTP 请求体验证（Gin 自动触发）
- `internal/config/config.go` — 配置文件加载后的结构验证
- `internal/tool/*/` — 工具输入参数验证

---

### 2.8 测试框架

需要覆盖单元测试（各模块 interface 的 mock 测试）和集成测试（Engine 完整流程）。

#### 候选库对比

| 维度 | testing + testify | goconvey | gomega + ginkgo |
|------|-------------------|----------|-----------------|
| Stars | 23k+ (testify) | 8k+ | 8k+ (gomega) |
| 断言风格 | assert.Equal | So(x, ShouldEqual, y) | Expect(x).To(Equal(y)) |
| Mock 支持 | testify/mock | 无 | gomock |
| 代码生成 | mockery | — | mockgen |
| 学习曲线 | 低 | 中 | 高 (BDD 风格) |
| 与 go test 兼容 | 完全 | 完全 | 完全 |
| 社区采用率 | 最高 | 中 | 中 |

#### 选定：testing (stdlib) + testify (`github.com/stretchr/testify`)

**选定理由：**
1. **事实标准** — Go 社区最广泛使用的测试辅助库
2. **断言简洁** — `assert.Equal(t, expected, actual)` 比 `if got != want { t.Errorf(...) }` 清晰得多
3. **Mock 生态** — `testify/mock` + `mockery` 代码生成，可为 `Provider`、`Storage` 等 interface 自动生成 mock
4. **Suite 支持** — `testify/suite` 提供 SetupTest/TeardownTest 生命周期，适合集成测试

**使用场景：**
- 单元测试：各 interface 实现的独立测试（mock 依赖）
- 集成测试：Engine → Provider → Tool 完整流程
- Mock 生成：`mockery` 为 `Channel`、`Provider`、`Tool`、`Storage` 等 interface 生成 mock

**辅助工具推荐：**
- `github.com/vektra/mockery/v2` — 根据 interface 自动生成 testify mock 代码
- `github.com/jarcoal/httpmock` — 拦截 HTTP 请求，用于 Provider 层测试

---

### 2.9 事件总线

架构文档定义了 `EventBus` 用于 Session 和 Router 之间的异步解耦通信。

#### 候选库对比

| 维度 | Go 原生 (chan + sync) | asaskevich/EventBus | mustafaturan/bus |
|------|----------------------|---------------------|------------------|
| 外部依赖 | 无 | 需要 | 需要 |
| 类型安全 | 通过泛型可实现 | 基于 interface{} | 基于 interface{} |
| 性能 | 最优 | 有反射开销 | 有反射开销 |
| 功能 | 需手写 | 发布/订阅/取消 | 发布/订阅/中间件 |
| 可控性 | 完全可控 | 库控制 | 库控制 |

#### 选定：标准库 `chan` + `sync` 模式（不引入外部依赖）

**选定理由：**
1. **场景简单** — 当前仅需 Session 状态变更通知和 Router 事件广播，不需要复杂的发布/订阅系统
2. **类型安全** — Go 1.21+ 泛型可实现类型安全的事件 channel，外部 EventBus 库多基于 `interface{}` 反射
3. **性能最优** — channel 是 Go 运行时原语，无反射开销
4. **完全可控** — 不依赖外部库的生命周期和 bug 修复节奏

**使用场景：**
- `internal/event/bus.go` — 泛型 EventBus 实现
- Session 超时/归档事件 → Storage 层处理
- Channel 连接/断开事件 → Session 层处理

**扩展预留：** 如未来需要跨进程事件（如多实例部署），可引入 Redis Pub/Sub 或 NATS，EventBus interface 保持不变。

---

### 2.10 CLI / 进程管理

BashTool 需要通过 `os/exec` 启动子进程执行 Shell 命令，并做超时控制和输出捕获。

#### 候选库对比

| 维度 | os/exec (stdlib) | cobra | urfave/cli |
|------|------------------|-------|------------|
| 定位 | 进程执行 | CLI 框架 | CLI 框架 |
| 我们的需求 | BashTool 子进程 | 未来 CLI 界面 | 未来 CLI 界面 |
| 外部依赖 | 无 | 需要 | 需要 |

#### 选定：标准库 `os/exec`

**选定理由：**
1. **完全满足需求** — BashTool 需要启动子进程、设置超时、捕获 stdout/stderr，`os/exec` + `context.WithTimeout` 足够
2. **无需 CLI 框架** — 当前项目是服务端程序，入口在 `cmd/server/main.go`，不需要复杂的 CLI 参数解析

**使用场景：**
- `internal/tool/bash/` — Shell 命令执行
- `internal/tool/grep/` — 调用 ripgrep 二进制
- `internal/tool/glob/` — 文件搜索（也可纯 Go `filepath.WalkDir` 实现）

**扩展预留：** 如未来需要提供 CLI 界面（类似原 TypeScript 版的 REPL），可引入 `cobra` 做命令解析，`bubbletea` 做终端 UI。

---

### 2.11 错误处理

需要统一的错误包装、错误链追踪、错误码体系。

#### 候选库对比

| 维度 | fmt.Errorf %w (stdlib) | pkg/errors | cockroachdb/errors |
|------|------------------------|------------|---------------------|
| 栈追踪 | 无 | 有 | 有 |
| 错误链 (Wrap/Unwrap) | 原生支持 | 支持 | 支持 |
| errors.Is / errors.As | 原生支持 | 支持 | 支持 |
| 维护状态 | Go 标准库 | 已归档 (deprecated) | 活跃 |
| 外部依赖 | 无 | 需要 | 需要（重） |
| Sentinel 错误 | 支持 | 支持 | 支持 |

#### 选定：标准库 `fmt.Errorf` + `errors.Is` / `errors.As`

**选定理由：**
1. **Go 1.13+ 原生足够** — `%w` 动词支持错误包装，`errors.Is` / `errors.As` 支持错误链检查
2. **pkg/errors 已废弃** — 官方 README 推荐迁移到标准库方案
3. **不需要栈追踪** — 结合 Zap 的结构化日志，错误发生时记录足够上下文信息即可定位问题
4. **零外部依赖** — 减少供应链风险

**使用场景：**
- `pkg/errors/errors.go` — 定义项目级 sentinel 错误（`ErrSessionNotFound`、`ErrToolTimeout` 等）
- 各层通过 `fmt.Errorf("operation failed: %w", err)` 逐层包装
- 最外层（Channel/Router）通过 `errors.Is` 判断错误类型，转换为用户友好消息

---

### 2.12 HTTP 客户端

Provider 层调用 LLM API 需要可靠的 HTTP 客户端，支持重试、退避、超时。

#### 候选库对比

| 维度 | hashicorp/go-retryablehttp | go-resty/resty | net/http (stdlib) |
|------|---------------------------|----------------|-------------------|
| Stars | 2k+ | 10k+ | — |
| 自动重试 | 内建 | 需手写中间件 | 无 |
| 指数退避 | 内建 | 需手写 | 无 |
| 可重试判断 | 可自定义 | 无 | 无 |
| 日志集成 | LeveledLogger 接口 | 无 | 无 |
| 连接池 | 复用 http.Transport | 复用 http.Transport | 原生 |
| API 风格 | 薄封装 http.Client | 链式 Builder | 原生 |

#### 选定：hashicorp/go-retryablehttp (`github.com/hashicorp/go-retryablehttp`)

**选定理由：**
1. **内建重试与退避** — LLM API 调用可能因限流（429）或瞬时故障（500/502/503）失败，自动重试是刚需
2. **可自定义策略** — 可配置最大重试次数、退避策略、哪些状态码可重试
3. **薄封装** — 底层仍是 `*http.Client`，不引入额外抽象，与 Bifrost SDK 或手写 adapter 均可配合
4. **日志集成** — 提供 `LeveledLogger` 接口，可注入 Zap Logger 记录重试过程

**使用场景：**
- `internal/provider/bifrost/adapter.go` — 传入 retryable HTTP client 给 Bifrost SDK
- `internal/provider/anthropic/` — 直连 Anthropic API 时使用
- `internal/tool/webfetch/` — WebFetchTool 抓取外部 URL

---

### 2.13 UUID 生成

会话 ID、消息 ID、工具调用 ID 均需要唯一标识符。

#### 选定：google/uuid (`github.com/google/uuid`)

| 维度 | 说明 |
|------|------|
| Stars | 5k+ |
| 标准合规 | RFC 9562 (UUID v1-v7) |
| 性能 | 高效，v4 基于 crypto/rand |
| 维护状态 | Google 官方维护 |

**选定理由：** Go 生态中 UUID 生成的唯一标准选择，无需对比替代方案。

**使用场景：**
- `internal/engine/session/manager.go` — 生成 Session ID
- `internal/engine/session/message.go` — 生成 Message ID
- `pkg/types/tool.go` — 生成 ToolCall ID

---

### 2.14 JWT

如未来需要 Bridge 系统（IDE 集成）的认证，或 HTTP API Channel 的 token 认证。

#### 选定：golang-jwt/jwt (`github.com/golang-jwt/jwt/v5`)

| 维度 | 说明 |
|------|------|
| Stars | 7k+ |
| 标准合规 | RFC 7519 |
| 算法支持 | HMAC, RSA, ECDSA, EdDSA |
| 维护状态 | 活跃（从 dgrijalva/jwt-go fork 并接管） |

**选定理由：** 社区标准，从已废弃的 `dgrijalva/jwt-go` 正式接管，活跃维护。

**使用场景：**
- `internal/router/middleware/auth.go` — JWT token 验证中间件
- 未来 Bridge 系统 — IDE 扩展认证

---

### 2.15 Markdown 处理

工具输出、LLM 响应可能包含 Markdown 内容，某些 Channel（如飞书）需要转换格式。

#### 候选库对比

| 维度 | goldmark | blackfriday/v2 | gomarkdown |
|------|----------|----------------|------------|
| Stars | 3.7k+ | 5.4k+ | 1k+ |
| CommonMark 合规 | 完全 | 不完全 | 不完全 |
| 可扩展性 | 极好（AST 插件） | 有限 | 有限 |
| GFM 支持 | 通过扩展 | 内建 | 内建 |
| 性能 | 优秀 | 优秀 | 一般 |

#### 选定：goldmark (`github.com/yuin/goldmark`)

**选定理由：**
1. **CommonMark 合规** — 解析结果与标准一致，无意外行为
2. **可扩展性** — AST 级插件系统，可定制渲染器（如输出飞书富文本格式）
3. **Hugo 选择** — Go 最大的静态站点生成器 Hugo 使用 goldmark，验证充分

**使用场景：**
- `internal/channel/feishu/` — Markdown → 飞书富文本消息格式转换
- 未来：Markdown → HTML 渲染（如 Web 前端展示）

---

### 2.16 并发工具

引擎层需要并发执行只读工具、管理多 goroutine 生命周期、处理超时和取消。

#### 候选库对比

| 维度 | golang.org/x/sync | sourcegraph/conc | 标准库 sync |
|------|-------------------|------------------|-------------|
| errgroup | 有 | 有（增强版） | 无 |
| singleflight | 有 | 无 | 无 |
| panic 安全 | 无 | 有（自动恢复） | 无 |
| 并发池 | 无 | Pool / Stream | 无 |
| WaitGroup | 标准 | 增强 | 有 |
| 维护 | Go 官方 | Sourcegraph | Go 标准库 |

#### 选定：golang.org/x/sync + sourcegraph/conc

**选定理由：**
1. **errgroup** — 并发执行多个只读工具，任一失败则全部取消，完美匹配工具并发执行场景
2. **singleflight** — 避免重复 LLM API 调用（如相同 prompt 的并发请求合并）
3. **conc** — 补充 panic 安全的并发原语，避免单个工具 panic 导致整个 Engine 崩溃

**使用场景：**
- `internal/engine/engine.go` — 并发执行只读工具（`errgroup`）
- `internal/provider/` — 相同请求去重（`singleflight`）
- `internal/tool/registry.go` — 工具并发执行池（`conc.Pool`）

---

## 3. go.mod 依赖清单

```go
module github.com/anthropic/claude-code-go

go 1.23.0

require (
    // === HTTP 框架 ===
    github.com/gin-gonic/gin                       v1.10.0

    // === WebSocket ===
    github.com/gorilla/websocket                    v1.5.3

    // === 配置管理 ===
    github.com/spf13/viper                          v1.19.0

    // === 结构化日志 ===
    go.uber.org/zap                                 v1.27.0

    // === LLM 供应商 ===
    github.com/maximhq/bifrost                      v0.2.0
    github.com/anthropics/anthropic-sdk-go           v0.2.0-beta.3

    // === 数据库 ===
    modernc.org/sqlite                              v1.34.5

    // === Schema 验证 (Gin 内建, 显式声明保证版本) ===
    github.com/go-playground/validator/v10           v10.23.0

    // === 测试增强 ===
    github.com/stretchr/testify                     v1.10.0

    // === HTTP 客户端 ===
    github.com/hashicorp/go-retryablehttp           v0.7.7

    // === UUID ===
    github.com/google/uuid                          v1.6.0

    // === JWT ===
    github.com/golang-jwt/jwt/v5                    v5.2.1

    // === Markdown ===
    github.com/yuin/goldmark                        v1.7.8

    // === 并发工具 ===
    golang.org/x/sync                               v0.10.0
    github.com/sourcegraph/conc                     v0.3.0
)
```

**说明：**
- 版本号基于 2026 年 4 月各库的预估最新稳定版，实际使用时以 `go get` 拉取的最新版为准
- 间接依赖（如 Viper 依赖的 `fsnotify`、`mapstructure` 等）由 `go mod tidy` 自动管理
- 测试专用依赖（如 `httpmock`、`mockery`）通过 `go install` 安装工具链，不进入 go.mod

---

## 4. 依赖治理策略

### 4.1 版本锁定策略

| 策略 | 说明 |
|------|------|
| go.sum 必须提交 | 确保所有环境依赖哈希一致 |
| 禁止 `go get -u ./...` 盲目升级 | 每次升级必须单独 PR + 测试 |
| 间接依赖锁定 | 通过 `go mod tidy` 维护，不手动编辑间接依赖版本 |
| 预发布版本限制 | 除非有明确理由，不使用 `-alpha`、`-beta`、`-rc` 版本 |

### 4.2 安全扫描

```bash
# 每次 CI 构建时执行
govulncheck ./...

# 本地开发建议定期执行
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

- **CI 集成** — 在 GitHub Actions / GitLab CI 中加入 `govulncheck` 步骤
- **阻断策略** — 发现 HIGH 或 CRITICAL 级别漏洞时阻断合并
- **定期扫描** — 每周自动运行一次全量扫描（即使无代码变更）

### 4.3 更新节奏

| 类型 | 频率 | 流程 |
|------|------|------|
| 安全补丁 | 立即 | 发现后 24h 内提交 PR |
| Bug 修复 (patch) | 每月 | 月度依赖更新批次 |
| 功能更新 (minor) | 每季度 | 评估 changelog 后升级 |
| 大版本 (major) | 按需 | 独立评估，充分测试后迁移 |

### 4.4 CGO 策略

| 规则 | 说明 |
|------|------|
| **默认禁用 CGO** | `CGO_ENABLED=0` 作为构建默认值 |
| **SQLite 选择纯 Go** | 使用 `modernc.org/sqlite` 而非 `mattn/go-sqlite3` |
| **例外审批** | 如确需 CGO 依赖，必须在本文档记录理由并经团队评审 |
| **CI 验证** | CI 中分别以 `CGO_ENABLED=0` 和 `CGO_ENABLED=1` 构建，确保零 CGO 依赖 |

### 4.5 许可证合规

| 优先级 | 许可证类型 | 策略 |
|--------|-----------|------|
| 推荐 | MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC | 直接使用 |
| 允许 | MPL-2.0 | 评估后使用（文件级 copyleft） |
| 禁止 | GPL-2.0, GPL-3.0, AGPL-3.0, SSPL | 不得引入 |
| 未知 | 无许可证 / 自定义许可证 | 必须法务评审 |

**扫描工具推荐：**
```bash
go install github.com/google/go-licenses@latest
go-licenses check ./...
```

### 4.6 依赖引入准则

引入新依赖前必须回答以下问题：

1. **标准库能否解决？** — 优先使用标准库
2. **维护状态如何？** — 最近 6 个月内有 commit，有明确的维护者
3. **依赖传递链有多深？** — 避免引入"依赖炸弹"（一个库带入几十个间接依赖）
4. **许可证是否合规？** — 参见上表
5. **是否有已知漏洞？** — `govulncheck` 检查通过
6. **社区采用度？** — 优先选择 Stars > 1k、在知名项目中使用的库

---

## 5. NPM → Go 映射参考

以下是原 TypeScript (Claude Code) 项目核心依赖到 Go 等价库的映射，供开发迁移时参考。

| TypeScript 依赖 | 用途 | Go 等价 | 备注 |
|-----------------|------|---------|------|
| `@anthropic-ai/sdk` | Anthropic API 调用 | `maximhq/bifrost` / `anthropic-sdk-go` | Bifrost 统一多 Provider |
| `commander.js` | CLI 参数解析 | `spf13/cobra` | 当前不需要，预留 |
| `react` + `ink` | 终端 UI 渲染 | `charmbracelet/bubbletea` + `lipgloss` | 当前不需要，预留 |
| `zod/v4` | Schema 验证 | `go-playground/validator/v10` | struct tag 驱动 |
| `@modelcontextprotocol/sdk` | MCP 协议 | `github.com/mark3labs/mcp-go` | Go MCP SDK |
| `lodash-es` | 集合操作工具函数 | `github.com/samber/lo` / 标准库 | 泛型 lodash 风格工具函数 |
| `chalk` | 终端颜色 | `github.com/fatih/color` / `lipgloss` | 当前服务端不需要 |
| `@opentelemetry/*` | 可观测性 | `go.opentelemetry.io/otel` | 官方 Go SDK |
| `strip-ansi` | ANSI 转义码清理 | `github.com/acarl005/stripansi` | 工具输出清理 |
| Express / http-server | HTTP 服务 | `gin-gonic/gin` | 本项目选定框架 |
| `ws` | WebSocket | `gorilla/websocket` | 本项目选定库 |
| `better-sqlite3` | SQLite 驱动 | `modernc.org/sqlite` | 纯 Go，无 CGO |
| Viper (对应无) | 配置管理 | `spf13/viper` | YAML + ENV |
| Zap (对应无) | 结构化日志 | `go.uber.org/zap` | 高性能结构化日志 |
| `jsonwebtoken` | JWT | `golang-jwt/jwt/v5` | token 签发/验证 |
| `marked` / `markdown-it` | Markdown 解析 | `yuin/goldmark` | CommonMark 合规 |
| `p-queue` / `p-limit` | 并发控制 | `golang.org/x/sync/errgroup` / `conc` | 原生并发原语 |
| `glob` | 文件 glob | `filepath.Glob` (stdlib) / `doublestar` | 标准库通常足够 |
| `execa` | 子进程执行 | `os/exec` (stdlib) | 标准库足够 |
| `retry` / `p-retry` | HTTP 重试 | `hashicorp/go-retryablehttp` | 内建退避策略 |

---

## 6. Revision History

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-04-05 | v1.0.0 | 初始依赖选型文档，覆盖 16 个功能域 |
