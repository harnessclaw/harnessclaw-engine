# HarnessClaw Engine

使用Go语言构建的 LLM 编程助手引擎。通过 WebSocket 协议对外提供能力，支持多轮对话、工具调用、权限管控和技能扩展。

## 架构概览

```
┌───────────-──┐   ┌─────────────┐   ┌─────────────┐
│  WebSocket   │   │    HTTP     │   │   Feishu    │
│  Channel     │   │   Channel   │   │  Channel    │
└──────┬───────┘   └──────┬──────┘   └──────┬──────┘
       │                  │                  │
       └──────────────────┼──────────────────┘
                          ▼
                ┌──────────────────┐
                │  Router + 中间件  │  Auth / RateLimit / Logging
                └────────┬─────────┘
                         ▼
                ┌──────────────────┐
                │   Query Engine   │  5-Phase Loop
                │  (queryloop.go)  │  预处理 → LLM流式调用 → 错误恢复
                └───┬──────────┬───┘  → 工具执行 → 续行判断
                    │          │
              ┌─────▼──-─┐  ┌──▼──────────┐
              │ Provider │  │ Tool System │
              │ (LLM)    │  │ 7 Built-in  │
              └───────-──┘  └─────────────┘
```

**依赖方向**: Channel → Router → Engine → Provider / Tool（单向，无循环依赖）

## 核心特性

- **5 阶段查询循环** — 预处理(自动压缩) → LLM 流式调用 → 错误恢复(指数退避重试) → 工具执行(并行/串行) → 续行判断
- **WebSocket 协议 v1.4** — 显式会话握手、完整消息生命周期、双向工具调用(服务端执行 + 客户端执行)、权限请求/响应流
- **7 个内置工具** — Bash、FileRead、FileEdit、FileWrite、Grep、Glob、WebFetch
- **6 步权限管线** — DenyRule → ToolCheckPerm → BypassMode → AlwaysAllowRule → ReadOnlyAutoAllow → ModeDefault，支持 6 种权限模式
- **技能系统** — 从 `SKILL.md` 文件加载技能，支持 YAML frontmatter、参数替换、优先级覆盖
- **多 Provider 支持** — 直连 Anthropic SSE 客户端 + Bifrost 多 Provider 适配器(Anthropic/OpenAI/Bedrock/Vertex)
- **上下文压缩** — 基于 LLM 的对话摘要 + 断路器模式，token 使用率达阈值时自动触发
- **会话管理** — 线程安全的会话状态、多连接 fan-out、空闲超时回收

## 项目结构

```
go_rebuild/
├── cmd/server/           # 入口 & 集成测试
│   ├── main.go           # 11 步启动流程
│   └── main_test.go      # E2E 测试 (build tag: integration)
├── configs/
│   └── config.yaml       # 默认配置
├── internal/
│   ├── channel/           # 多协议接入层 (WebSocket / HTTP / Feishu)
│   ├── command/           # 命令注册 & 优先级系统
│   ├── config/            # Viper 配置管理 (50+ 默认项)
│   ├── engine/            # 核心查询引擎
│   │   ├── queryloop.go   # QueryEngine 主循环 (831 行)
│   │   ├── executor.go    # 工具并行/串行执行器
│   │   ├── compact/       # LLM 上下文压缩
│   │   ├── context/       # 系统提示词组装
│   │   └── session/       # 会话状态 & 生命周期
│   ├── event/             # 进程内发布/订阅事件总线
│   ├── permission/        # 6 步权限管线 (6 种模式)
│   ├── provider/          # LLM Provider 抽象
│   │   ├── anthropic/     # Anthropic SSE 直连客户端
│   │   ├── bifrost/       # 多 Provider 适配器
│   │   └── retry/         # 指数退避 + 529 过载切换
│   ├── router/            # 消息路由 + 中间件链
│   ├── skill/             # SKILL.md 加载 & 参数替换
│   ├── storage/           # 存储接口 (内存实现)
│   └── tool/              # 工具系统
│       ├── tool.go        # Tool 接口 + 10 个扩展接口
│       ├── registry.go    # 线程安全工具注册表
│       ├── pool.go        # 不可变 per-query 工具池
│       └── bash/fileread/fileedit/filewrite/grep/glob/webfetch/skilltool/
├── pkg/
│   ├── types/             # 共享类型 (Message, Event, ToolCall, Context)
│   └── errors/            # 领域错误 (16 个错误码)
├── docs/
│   ├── protocols/         # WebSocket 协议规范 (v1.4)
├── Makefile               # 构建/运行/测试/lint
└── go.mod                 # Go 1.26.1
```

## 快速开始

### 前置条件

- Go 1.26+
- (可选) [golangci-lint](https://golangci-lint.run/) — 用于代码检查
- (可选) [ripgrep](https://github.com/BurntSushi/ripgrep) — Grep 工具运行时依赖

### 构建 & 运行

```bash
# 构建
make build              # 输出 ./dist/harnessclaw-engine

# 运行 (使用默认配置)
make run                # go run ./cmd/server -config ./configs/config.yaml

# 也可以直接指定配置文件
./dist/harnessclaw-engine -config ./configs/config.yaml
```

### 测试

```bash
# 单元测试
make test               # go test ./... -v -race -count=1

# 覆盖率报告
make test-cover         # 生成 coverage.html

# 集成测试 (需要真实 LLM API)
go test -tags=integration ./cmd/server/ -v
go test -tags=integration ./internal/provider/bifrost/ -v
```

### 其他命令

```bash
make fmt                # 格式化代码
make tidy               # 整理 go.mod
make lint               # 代码检查
make vuln               # 漏洞扫描
make clean              # 清理构建产物
```

## 配置

配置文件 `configs/config.yaml`，主要配置项：

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `server.port` | HTTP 服务端口 | `8080` |
| `channels.websocket.port` | WebSocket 端口 | `8081` |
| `channels.websocket.path` | WebSocket 路径 | `/ws` |
| `llm.default_provider` | LLM Provider | `anthropic` |
| `llm.providers.anthropic.model` | 模型名称 | `astron-code-latest` |
| `engine.max_turns` | 单轮最大工具调用次数 | `50` |
| `engine.auto_compact_threshold` | 自动压缩 token 阈值比例 | `0.8` |
| `session.idle_timeout` | 会话空闲超时 | `30m` |
| `permission.mode` | 权限模式 | `default` |
| `tools.*` | 各工具开关 | 全部 `true` |

## WebSocket 协议

连接地址: `ws://host:8081/ws`

### 会话生命周期

```
客户端                                   服务端
  │                                        │
  │── session.create ──────────────────────>│
  │<────────────────────── session.created ─│
  │                                        │
  │── user.message ────────────────────────>│
  │<──────────────────── message.start ─────│
  │<──── content.start / content.delta ─────│  (流式文本)
  │<──────── tool.start / tool.end ─────────│  (服务端工具)
  │<──────────── tool.call ─────────────────│  (客户端工具)
  │── tool.result ─────────────────────────>│
  │<──── permission.request ────────────────│  (权限确认)
  │── permission.response ─────────────────>│
  │<──────────────── content.stop ──────────│
  │<──────────────── message.stop ──────────│
  │<──────────────── task.end ──────────────│
  │                                        │
  │── abort ───────────────────────────────>│  (中断)
```

详细协议规范见 [docs/protocols/websocket.md](docs/protocols/websocket.md)。

## 文档

- [WebSocket 协议规范 v1.4](docs/protocols/websocket.md)

## License

Apache-2.0 License. See [LICENSE](LICENSE) for details.
