# Go Rebuild — UML 流程图文档

> 版本: v1.0.0 | 日期: 2026-04-05 | 作者: 架构组

本文档使用 Mermaid 语法描述系统各核心流程的 UML 图，作为 [ARCHITECTURE.md](docs/refactor/ARCHITECTURE.md) 的补充。所有流程图均基于架构文档中定义的分层结构（Channel → Router → Engine → Provider/Tool）和核心 Interface 设计。

---

## 1. 消息处理主流程

本流程图展示一条用户消息从接入到响应的完整生命周期。核心设计思路是：Channel 层负责协议适配，Router 层负责标准化与中间件链处理，Engine 层承担查询循环（LLM 调用 → 工具执行 → 续行判断），最终流式事件通过 Channel 返回给用户。关键决策点在于 `StopReason` 的判断——当 LLM 返回 `tool_use` 时，Engine 进入工具执行分支并自动续行，直到 LLM 返回 `end_turn` 或达到最大轮次限制。

```mermaid
sequenceDiagram
    autonumber
    actor User as 用户
    participant CH as Channel<br/>Feishu/WS/HTTP
    participant RT as Router
    participant MW as Middleware Chain<br/>认证/限流/日志
    participant SM as Session Manager
    participant ST as Storage<br/>SQLite
    participant EG as Engine
    participant CB as Context Builder
    participant PV as Provider<br/>Bifrost
    participant LLM as LLM API
    participant TR as Tool Registry
    participant PM as Permission
    participant TL as Tool
    participant CP as Compactor

    User ->> CH: 发送消息
    CH ->> RT: IncomingMessage（标准化格式）
    RT ->> MW: 传递请求
    MW ->> MW: Auth 认证校验
    MW ->> MW: RateLimit 限流检查
    MW ->> MW: Logging 记录请求日志
    MW ->> SM: 查找/创建会话

    SM ->> ST: 加载历史消息（如需恢复）
    ST -->> SM: 会话数据

    SM ->> EG: ProcessMessage(sessionID, msg)
    EG ->> CB: 组装系统提示 + 用户上下文
    CB -->> EG: 完整消息列表

    EG ->> PV: Chat(req) 流式调用
    PV ->> LLM: messages.create (streaming)
    LLM -->> PV: StreamEvent (text_delta)
    PV -->> EG: StreamEvent
    EG -->> CH: EngineEvent（文本块）
    CH -->> User: 实时响应文本

    LLM -->> PV: StreamEvent (tool_use)
    PV -->> EG: StreamEvent (tool_use)

    rect rgb(255, 245, 230)
        Note over EG, TL: 工具调用分支
        EG ->> TR: findTool(name)
        TR ->> PM: checkPermission(tool, session)
        PM -->> TR: Allow
        TR -->> EG: Tool 实例
        EG ->> TL: Execute(ctx, input)
        TL -->> EG: ToolResult
        EG ->> PV: 续行调用（含 tool_result）
        PV ->> LLM: messages.create (含历史 + tool_result)
        LLM -->> PV: StreamEvent (text/tool_use/end_turn)
        PV -->> EG: StreamEvent
    end

    EG ->> CP: autoCompactIfNeeded()
    CP -->> EG: 压缩结果（如触发）

    EG -->> SM: 更新会话（追加消息 + token 计数）
    EG -->> CH: EngineEvent (done)
    CH -->> User: 响应完成
```

---

## 2. 工具调用详细流程

本流程图展示工具执行的完整决策树。关键设计决策包括：只读工具（如 `GrepTool`、`GlobTool`、`FileReadTool`）可进入并发执行池以提高吞吐量，而写入工具（如 `BashTool`、`FileEditTool`、`FileWriteTool`）必须串行执行以避免竞态条件。所有工具执行均受 `context.WithTimeout` 保护，超时时间由 `engine.tool_timeout` 配置项控制（默认 120 秒）。

```mermaid
flowchart TD
    A["LLM 返回 tool_use"] --> B["解析 ToolCall<br/>name + input"]
    B --> C{"ToolRegistry.Find(name)<br/>工具是否存在?"}
    C -- "不存在" --> D["返回错误 ToolResult<br/>IsError=true<br/>未知工具: name"]
    C -- "存在" --> E{"PermissionChecker.Check()<br/>权限是否通过?"}
    E -- "Deny 拒绝" --> F["返回权限拒绝 ToolResult<br/>IsError=true<br/>权限不足"]
    E -- "Allow 放行" --> G{"tool.IsReadOnly()?"}
    G -- "ReadOnly 只读" --> H["进入并发执行池"]
    G -- "Write 写入" --> I["进入串行执行队列"]
    H --> J["context.WithTimeout<br/>toolTimeout"]
    I --> J
    J --> K["tool.Execute(ctx, input)"]
    K --> L{"执行是否超时?"}
    L -- "超时" --> M["返回超时错误 ToolResult<br/>IsError=true"]
    L -- "未超时" --> N{"执行是否成功?"}
    N -- "Error 错误" --> O["包装错误 ToolResult<br/>IsError=true"]
    N -- "Success 成功" --> P["返回正常 ToolResult<br/>IsError=false"]
    M --> Q["ToolResult 加入消息历史"]
    O --> Q
    P --> Q
    Q --> R{"检查 StopReason<br/>是否需要续行?"}
    R -- "stop_reason=tool_use<br/>需要续行" --> S["将 tool_result 拼入消息<br/>重新调用 LLM"]
    R -- "stop_reason=end_turn<br/>对话结束" --> T["返回最终响应"]

    style D fill:#ffcdd2
    style F fill:#ffcdd2
    style M fill:#ffcdd2
    style O fill:#ffcdd2
    style P fill:#c8e6c9
    style H fill:#e3f2fd
    style I fill:#fff3e0
```

---

## 3. 多 Channel 路由流程

本流程图展示多 Channel 消息如何统一汇聚到 Router 层。架构的核心设计是：每个 Channel 独立运行在自己的 goroutine 中，负责协议解析和消息提取，然后构建标准化的 `IncomingMessage`（包含消息体、来源标识、会话标识）。Router 接收所有 Channel 的标准化消息后，依次经过中间件链处理，最终交给 Engine。新增 Channel 只需实现 `Channel` interface 并在启动时注册，无需修改核心代码。

```mermaid
flowchart LR
    subgraph FeishuInput["飞书接入"]
        FW["Feishu Webhook<br/>HTTP 回调"] --> FC["Feishu Channel"]
        FC --> FP["解析飞书事件<br/>提取消息体<br/>提取用户ID<br/>提取会话ID"]
        FP --> FM["构建 IncomingMessage<br/>Source=feishu"]
    end

    subgraph WSInput["WebSocket 接入"]
        WC["WebSocket 连接"] --> WSH["WS Channel"]
        WSH --> WSP["解析 WS 帧<br/>提取消息体<br/>提取连接ID"]
        WSP --> WSM["构建 IncomingMessage<br/>Source=websocket"]
    end

    subgraph HTTPInput["HTTP API 接入"]
        HP["HTTP POST<br/>/api/v1/chat"] --> HC["HTTP Channel"]
        HC --> HPP["解析请求体<br/>提取消息体<br/>提取 API Key"]
        HPP --> HM["构建 IncomingMessage<br/>Source=http"]
    end

    FM --> RT["Router<br/>统一 IncomingMessage"]
    WSM --> RT
    HM --> RT

    RT --> MW0["Middleware: Auth<br/>认证校验"]
    MW0 --> MW1["Middleware: RateLimit<br/>限流检查"]
    MW1 --> MW2["Middleware: Logging<br/>请求日志"]
    MW2 --> HD["Handler<br/>Engine.ProcessMessage()"]

    style FeishuInput fill:#e1f5fe
    style WSInput fill:#e8f5e9
    style HTTPInput fill:#fff3e0
    style RT fill:#fff9c4
    style HD fill:#fce4ec
```

---

## 4. 权限检查流程

本流程图展示工具权限检查的完整决策逻辑。权限系统采用三级判定：首先检查工具级开关（`config.tools.X.enabled`），然后检查会话级权限覆盖（允许单个会话临时放行或禁止特定工具），最后回落到全局权限模式判断。全局模式支持 `bypassPermissions`（全部放行）、`plan`（只读放行，写入需确认）、`default`（根据工具类型判断）三种策略。这种分层设计既保证了安全性，又提供了灵活的覆盖能力。

```mermaid
flowchart TD
    A["Tool 调用请求"] --> B["获取工具权限配置<br/>config.tools.X"]
    B --> C{"工具是否启用?<br/>config.tools.X.enabled"}
    C -- "No 未启用" --> D["Deny 拒绝<br/>工具已禁用"]
    C -- "Yes 已启用" --> E["检查会话级权限覆盖<br/>session.permissionOverrides"]
    E --> F{"是否有显式规则?"}
    F -- "Allow 显式允许" --> G["放行"]
    F -- "Deny 显式拒绝" --> H["拒绝"]
    F -- "None 无规则" --> I["检查全局权限模式<br/>config.permission_mode"]
    I --> J{"权限模式?"}
    J -- "bypassPermissions" --> K["全部放行"]
    J -- "plan" --> L{"工具类型?"}
    L -- "ReadOnly 只读" --> M["放行"]
    L -- "Write 写入" --> N["需要确认<br/>等待用户审批"]
    J -- "default" --> O{"工具类型?"}
    O -- "ReadOnly 只读" --> P["放行"]
    O -- "Write 写入" --> Q["需要确认<br/>等待用户审批"]

    style D fill:#ffcdd2
    style H fill:#ffcdd2
    style G fill:#c8e6c9
    style K fill:#c8e6c9
    style M fill:#c8e6c9
    style P fill:#c8e6c9
    style N fill:#fff3e0
    style Q fill:#fff3e0
```

---

## 5. 会话生命周期

本状态图展示会话从创建到销毁的完整生命周期。核心设计包括：活跃会话保存在内存中（带 LRU 淘汰机制），空闲超时后自动归档到 SQLite，归档会话可按需恢复。压缩状态（`Compacting`）是活跃状态的子过程——当 token 使用量超过阈值（默认 80%）时，Engine 触发自动压缩以释放上下文窗口空间。空闲超时默认为 30 分钟（由 `session.idle_timeout` 配置项控制）。

```mermaid
stateDiagram-v2
    [*] --> Created: 新消息到达（无现有会话）

    Created --> Active: 首次处理消息

    Active --> Active: 继续对话（新消息到达）
    Active --> Compacting: token 超阈值（80%）
    Compacting --> Active: 压缩完成

    Active --> Idle: 空闲超时倒计时开始

    Idle --> Active: 新消息到达（重置计时）
    Idle --> Archived: 空闲超时到期（30m）

    Archived --> Active: 会话恢复（从 SQLite 加载）
    Archived --> Terminated: 过期清理

    Active --> Terminated: 显式结束（用户或系统）

    Terminated --> [*]

    state Active {
        [*] --> 等待消息
        等待消息 --> 处理中: ProcessMessage
        处理中 --> LLM调用: 调用 Provider
        LLM调用 --> 工具执行: tool_use
        工具执行 --> LLM调用: 续行
        LLM调用 --> 等待消息: end_turn
    }

    note right of Compacting
        调用 LLM 生成摘要
        替换历史消息
        重置 token 计数
    end note

    note right of Archived
        会话数据持久化在 SQLite
        内存释放
    end note
```

---

## 6. 上下文压缩流程

本流程图展示上下文压缩的完整策略。压缩机制采用 circuit breaker 模式防止连续失败导致资源浪费——当连续压缩失败达到 3 次时，自动熔断并跳过后续压缩请求，直到成功重置。压缩策略分为两种：当消息数较少（< 10 条）时使用 micro compact（简单截断早期消息），当消息数较多时使用 full compact（调用 LLM 生成摘要替换历史）。阈值由 `engine.auto_compact_threshold` 配置项控制（默认 80%）。

```mermaid
flowchart TD
    A["查询循环开始"] --> B["计算当前 token 使用量"]
    B --> C{"使用率 > auto_compact_threshold<br/>默认 80%?"}
    C -- "No 未超阈值" --> D["继续正常处理"]
    C -- "Yes 超过阈值" --> E["触发自动压缩"]
    E --> F{"Circuit Breaker 检查<br/>连续失败次数 < 3?"}
    F -- "No 已熔断" --> G["跳过压缩<br/>继续处理<br/>记录警告日志"]
    F -- "Yes 可执行" --> H{"消息数量检查"}
    H -- "消息数 < 10" --> I["Micro Compact<br/>截断早期消息<br/>保留最近消息"]
    H -- "消息数 >= 10" --> J["Full Compact"]
    J --> K["调用 LLM 生成对话摘要"]
    K --> L["替换历史消息为摘要消息"]
    L --> M["重置 token 计数"]
    I --> N["更新 Circuit Breaker<br/>记录成功"]
    M --> N
    N --> D

    K -- "LLM 调用失败" --> O["更新 Circuit Breaker<br/>记录失败"]
    O --> G

    style D fill:#c8e6c9
    style G fill:#fff3e0
    style I fill:#e3f2fd
    style J fill:#e3f2fd
    style O fill:#ffcdd2
```

---

## 7. 启动与关闭流程

本流程图展示系统的优雅启动和关闭过程。启动阶段采用顺序初始化基础设施（Config → Logger → Storage → EventBus），然后注册工具和创建核心组件，最后并行启动所有 Channel。关闭阶段遵循相反顺序：先停止接收新请求，然后并行关闭所有 Channel，等待活跃查询在 grace period 内完成，持久化所有活跃会话，最后关闭存储和日志。这种设计确保了数据不丢失和连接不泄漏。

```mermaid
sequenceDiagram
    autonumber
    participant M as main.go
    participant CF as Config<br/>Viper
    participant LG as Logger<br/>Zap
    participant ST as Storage<br/>SQLite
    participant EB as EventBus
    participant TR as ToolRegistry
    participant PV as Provider<br/>Bifrost
    participant SM as SessionManager
    participant EG as Engine
    participant RT as Router
    participant FC as Feishu Channel
    participant WC as WS Channel
    participant HC as HTTP Channel
    participant OS as OS Signal

    rect rgb(232, 245, 233)
        Note over M, HC: 启动阶段
        M ->> CF: 加载配置（YAML + ENV）
        CF -->> M: Config 对象
        M ->> LG: 初始化 Zap Logger
        M ->> ST: 初始化 SQLite（建表/迁移）
        ST -->> M: Storage 实例
        M ->> EB: 创建事件总线
        M ->> TR: 注册所有工具<br/>Bash/FileRead/FileEdit/...
        M ->> PV: 初始化 Bifrost Adapter
        M ->> SM: 创建 SessionManager<br/>注入 Storage + EventBus
        M ->> EG: 创建 Engine<br/>注入 Provider + ToolRegistry + SessionManager
        M ->> RT: 创建 Router<br/>注入 Engine + Middleware 链
    end

    rect rgb(225, 245, 254)
        Note over M, HC: 并行启动 Channels
        par 并行启动
            M ->> FC: Start(ctx, handler)
            FC -->> M: 飞书 Webhook 监听中
        and
            M ->> WC: Start(ctx, handler)
            WC -->> M: WebSocket 监听中
        and
            M ->> HC: Start(ctx, handler)
            HC -->> M: HTTP API 监听中
        end
    end

    M ->> OS: 注册监听 SIGINT/SIGTERM
    Note over M: 服务运行中...

    rect rgb(255, 243, 224)
        Note over M, HC: 关闭阶段
        OS ->> M: 收到终止信号
        M ->> RT: 停止接收新请求

        par 并行关闭 Channels
            M ->> FC: Stop(ctx)
            FC -->> M: 飞书 Channel 已关闭
        and
            M ->> WC: Stop(ctx)
            WC -->> M: WS Channel 已关闭
        and
            M ->> HC: Stop(ctx)
            HC -->> M: HTTP Channel 已关闭
        end

        M ->> EG: 等待活跃查询完成<br/>grace period
        M ->> SM: 持久化所有活跃会话
        SM ->> ST: SaveSession (批量)
        M ->> ST: Close()
        M ->> LG: Sync()
    end

    Note over M: 进程退出
```

---

## 8. LLM Provider 调用流程

本流程图展示通过 Bifrost Adapter 进行多 Provider LLM 调用的详细过程。Bifrost 作为统一抽象层，将不同 Provider 的 API 差异（请求格式、SSE 事件结构、错误码）屏蔽在适配器内部，向 Engine 暴露统一的 `ChatStream` 接口。Engine 通过 channel 消费流式事件，根据事件类型分别处理文本增量、工具调用和消息结束。当直连模式启用时（Bifrost 不可用或配置为 fallback），Engine 直接调用对应 Provider 的 Adapter。

```mermaid
sequenceDiagram
    autonumber
    participant EG as Engine
    participant BA as Bifrost Adapter
    participant BS as Bifrost SDK
    participant AN as Anthropic API
    participant OA as OpenAI API
    participant TR as Tool Registry
    participant CH as Channel

    EG ->> BA: Chat(ChatRequest)
    BA ->> BS: 路由到配置的 Provider

    alt Anthropic Provider
        BS ->> AN: messages.create (stream=true)
        AN -->> BS: SSE: message_start
        AN -->> BS: SSE: content_block_delta (text)
        AN -->> BS: SSE: content_block_delta (tool_use)
        AN -->> BS: SSE: message_stop
    else OpenAI Provider
        BS ->> OA: chat.completions.create (stream=true)
        OA -->> BS: SSE: delta (content)
        OA -->> BS: SSE: delta (tool_calls)
        OA -->> BS: SSE: [DONE]
    end

    BS -->> BA: 统一 StreamEvent 格式
    BA -->> EG: ChatStream{Events chan}

    loop 消费流式事件
        EG ->> EG: 读取 ChatStream.Events

        alt TextDelta 事件
            EG ->> CH: 转发文本块<br/>EngineEvent{Type=text}
            CH ->> CH: 实时推送给用户
        else ToolUse 事件
            EG ->> TR: 查找并执行工具
            TR -->> EG: ToolResult
            Note over EG: 将 tool_result 加入消息历史
        else MessageEnd 事件
            EG ->> EG: 记录 Usage<br/>检查 StopReason
        end
    end

    alt StopReason = tool_use
        Note over EG, BA: 需要续行：携带 tool_result 再次调用
        EG ->> BA: Chat(续行请求)
        BA ->> BS: 新一轮流式调用
        BS -->> BA: StreamEvent...
        BA -->> EG: ChatStream
    else StopReason = end_turn
        Note over EG: 对话回合结束
        EG ->> CH: EngineEvent{Type=done}
    end
```

---

## Revision History

| 日期 | 版本 | 变更 |
|------|------|------|
| 2026-04-05 | v1.0.0 | 初始 UML 流程图文档，包含 8 个核心流程图 |
