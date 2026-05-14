# Providers Management API 参考文档

> **更新记录**
>
> | 日期 | 变更 |
> |------|------|
> | 2026-05-14 | 初版：`GET /api/v1/providers` / `GET|PUT /api/v1/providers/fallback-chain` / `PATCH /api/v1/providers/{name}` |
> | 2026-05-14 | `api_key` 改为明文返回（去掉脱敏），方便客户端 PATCH 表单回填 |
> | 2026-05-14 | 新增 `type` 字段（必填）解耦 yaml key 与 bifrost 后端协议；PATCH 支持改 `type` 热切换后端 |

Providers Management API 让客户端在运行时**热生效**地修改服务端的 LLM provider 配置 —— 改完立即作用于后续请求，同时持久化回 `config_self.yaml`，下次重启不丢失。

适用场景：

- 客户端面板里展示 / 切换 fallback chain 顺序
- 紧急情况下旋转某个 provider 的 api_key / base_url 不重启服务
- 把主 provider 换成另一个已声明的 provider

不适用：

- 新增 / 删除 provider —— 本期不做（要改 yaml + 等下次重启）
- 改 `llm.health.*` (cooldown / budget) —— 本期不做
- 单 provider 部署 —— 服务端 `fallback_chain` 留空或只 1 项时**不挂载本 API**（请求会 404）

---

- **Base URL**: `http://localhost:8090`（Console 管理端口，与 models / sessions / agents 同端口）
- **路由前缀**: `/api/v1/providers`
- **Content-Type**: `application/json`
- **认证**: 无（与现有 Console API 一致；后续如加 token 会单独通告）
- **响应包络**: 所有响应统一为 `{"code":"OK","data":{...}}`，错误形如 `{"code":"<error_code>","message":"..."}`

---

## 错误码

| code | HTTP Status | 说明 |
|------|-------------|------|
| `bad_request` | 400 | 请求体格式错误 / 路径不合法 / 空 patch |
| `update_failed` | 400 | 业务校验失败：未知 provider、空 chain、adapter 构建失败等 |
| `persist_failed` | 500 | 内存已更新但 yaml 写盘失败（见底部"语义"小节）|
| `method_not_allowed` | 405 | 该路径不支持此 HTTP method |

---

## 通用数据结构

### `Provider`（GET /providers 数组元素）

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | provider 名（= `llm.providers` 的 map key）。任意字符串，作为 chain 引用的标识 |
| `type` | enum | bifrost 后端协议（见下表"`type` 枚举"）|
| `model` | string | 模型 ID（按 provider 协议解释，如 `deepseek-v4-flash`）|
| `base_url` | string | provider HTTP endpoint |
| `api_key` | string | **明文** API key。本 API 不做脱敏，方便客户端 PATCH 表单回填编辑。⚠️ **务必在网络层限制 `/api/v1/providers*` 的访问**（内网 / 本机 / 反向代理鉴权），不要暴露到公网。 |
| `max_tokens` | int | 单次响应允许的最大 token 数 |
| `temperature` | float | 采样温度（0.0–2.0），0 表示沿用 provider 默认 |
| `in_chain` | bool | 该 provider 是否在当前 `fallback_chain` 中 |

### `type` 枚举

| 值 | 后端协议 | 典型用途 |
|----|---------|---------|
| `openai` | OpenAI Chat Completions API | OpenAI 自家 + 所有 OpenAI-compatible 厂商（DeepSeek / Kimi / GLM / MiniMax / 讯飞星火 / 通义千问 等），改 `base_url` 即可 |
| `anthropic` | Anthropic Messages API | Claude 系列；如有 Anthropic-compatible 网关也用这个 |
| `gemini` | Google Gemini API | Google Gemini 系列 |
| `azure` | Azure OpenAI Service | 部署在 Azure 上的 GPT 系列 |
| `bedrock` | AWS Bedrock | AWS 托管模型 |
| `cohere` | Cohere | Cohere 自家 |
| `vertex` | Google Vertex AI | GCP 托管 |
| `mistral` | Mistral API | Mistral 自家 |
| `ollama` | Ollama 本地 | 本地部署 / 自建 |
| `groq` | Groq | Groq 加速 |
| `openrouter` | OpenRouter 聚合 | OpenRouter 转发 |
| `perplexity` | Perplexity | Perplexity |
| `cerebras` | Cerebras | Cerebras |
| `huggingface` | HuggingFace Inference | HF Hub 推理 |

**重要**：
1. yaml 里 `llm.providers` 下的 map key（即本 API 中的 `name`）是**任意字符串**，用作 chain 引用 + 客户端展示
2. 多个 yaml key 可以共用同一个 `type`（典型场景：`claude-45` / `claude-46` 都 `type: anthropic`，但 `model` 不同 → 两个独立的 chain entry，独立的 trip / probe 状态）
3. 改 `type` 等于换后端协议；PATCH 后服务端会 rebuild adapter 立即生效

### `ChainEntry`（GET /fallback-chain 的 `entries[i]`）

| 字段 | 类型 | 说明 |
|------|------|------|
| `index` | int | 在 chain 中的位置，0 = 主 |
| `name` | string | 与 chain[index] 一致 |
| `state` | enum | `healthy` / `tripped` / `ready_to_probe`，**严格互斥** |
| `tripped_until` | string (RFC3339) | 冷却结束时间；从未 trip 时省略 |
| `cooldown_seconds` | int | 最近一次 trip 应用的冷却时长；从未 trip 时 0 |
| `consecutive_failures` | int | 自上次 recover 以来连续 trip 次数；成功一次就归零 |

### `state` 含义

| state | 含义 | 何时被路由 |
|-------|------|----------|
| `healthy` | 从未 trip 或已 recover | 优先路由 |
| `tripped` | 在冷却窗口内 | 不路由（cooldown 内 skip） |
| `ready_to_probe` | 冷却已过、待探活 | 下次请求作为探活候选（Probe budget=5s）|

---

## 端点

### 1. 列出所有 providers

`GET /api/v1/providers`

列出 `llm.providers` 下声明的全部 provider（**包括未在 chain 中的**），api_key 以明文返回。客户端用来：
- 渲染 provider 选择器
- 显示当前 model / base_url
- 标识哪些已在 chain 中（`in_chain`）

**响应**（200）：

```json
{
  "code": "OK",
  "data": {
    "providers": [
      {
        "name": "claude-46",
        "type": "anthropic",
        "model": "claude-sonnet-4-6",
        "base_url": "https://api.anthropic.com",
        "api_key": "sk-ant-***",
        "max_tokens": 16384,
        "in_chain": true
      },
      {
        "name": "deepseek",
        "type": "openai",
        "model": "deepseek-v4-flash",
        "base_url": "https://api.deepseek.com",
        "api_key": "sk-***",
        "max_tokens": 100000,
        "in_chain": true
      },
      {
        "name": "kimi",
        "type": "openai",
        "model": "kimi-k2",
        "base_url": "https://api.moonshot.cn/v1",
        "api_key": "sk-***",
        "max_tokens": 8192,
        "in_chain": false
      }
    ]
  }
}
```

排序：按 `name` 字典序升序，前端可按 `in_chain` / 收藏等再分组。

---

### 2. 查询当前 fallback chain

`GET /api/v1/providers/fallback-chain`

返回当前路由顺序 + 每个 entry 的实时健康状态。客户端用来：

- 展示链路顺序（chain[0] 是主）
- 标识哪些 provider 此刻在冷却（`state="tripped"`）
- 显示冷却倒计时（`tripped_until` 减去当前时间）

**响应**（200）：

```json
{
  "code": "OK",
  "data": {
    "chain": ["anthropic", "openai"],
    "entries": [
      {
        "index": 0,
        "name": "anthropic",
        "state": "tripped",
        "tripped_until": "2026-05-14T08:15:25Z",
        "cooldown_seconds": 60,
        "consecutive_failures": 2
      },
      {
        "index": 1,
        "name": "openai",
        "state": "healthy",
        "cooldown_seconds": 0,
        "consecutive_failures": 0
      }
    ]
  }
}
```

`chain[i]` 永远对应 `entries[i]`（同一索引）。`entries` 长度等于 chain 长度。

**实现备注**：状态是被动维护的 —— 服务端**不主动拨测** provider，只在真实请求路过时更新。所以"立刻刷新"GET 拿到的是上一次请求后的状态，不会触发新的 health check（也就不烧额外 token）。

---

### 3. 替换 fallback chain

`PUT /api/v1/providers/fallback-chain`

**请求体**：

```json
{ "chain": ["openai", "anthropic"] }
```

约束：

- `chain` 非空
- 所有 `chain[i]` 都必须出现在 `llm.providers` 中（否则 400 `update_failed`）
- chain 顺序就是优先级；`chain[0]` 是新主

**响应**（200）：响应体与 [GET fallback-chain](#2-查询当前-fallback-chain) **完全一致**，反映替换后的状态。客户端可以用响应直接刷新本地缓存，无需再 GET。

**副作用**：

1. 内存中 ProviderManager 立即 atomic-swap 新 Failover dispatcher
2. 后续真实 LLM 请求按新 chain 路由
3. 新 chain 替换时**会重置所有 provider 的健康状态**（旧的 trip / cooldown 信息丢失）—— 因为 dispatcher 整个换了新的
4. `configs/config_self.yaml` 的 `llm.fallback_chain` 字段被改写，注释 / 顺序 / 其他 key 全部保留

**错误**：

| 场景 | code | HTTP |
|------|------|------|
| 请求体不是合法 JSON | `bad_request` | 400 |
| `chain` 字段为空 / 不存在 | `bad_request` | 400 |
| chain 引用了未声明的 provider | `update_failed` | 400 |
| 内存改成功但 yaml 写盘失败 | `persist_failed` | 500（详见"语义"小节）|

---

### 4. 修改单个 provider

`PATCH /api/v1/providers/{name}`

**请求体**（所有字段可选，至少需要一个）：

```json
{
  "type":     "openai",
  "model":    "deepseek-chat",
  "api_key":  "sk-new-key-here",
  "base_url": "https://new.api.example.com"
}
```

约束：

- `{name}` 必须是 `llm.providers` 中已存在的 provider（404 ❌，是 400 `update_failed`，因为不存在视为业务校验失败）
- 至少传一个字段（空 patch 返回 400 `bad_request`）
- `type` 如果传，必须是 [`type` 枚举](#type-枚举) 中的值；未知值返回 400 `bad_request`
- `model` / `api_key` / `base_url` 接受**任意非空字符串**，服务端不校验 URL / model 是否真实可用 —— 验证时机是下次真实 LLM 请求打过去

**响应**（200）：响应体与 [GET providers](#1-列出所有-providers) **完全一致**（含全部 provider 的最新快照）。同样可以用响应直接刷新本地缓存。

**副作用**：

1. 服务端为 `{name}` 重建 Bifrost adapter（旧 adapter 优雅 Shutdown 释放连接）
2. 如果 `{name}` 在当前 chain 中 → 立即触发 dispatcher 重建，新 chain 用新 adapter
3. 如果 `{name}` 不在 chain 中 → 仅更新 cache + yaml，dispatcher 不动
4. `configs/config_self.yaml` 的 `llm.providers.{name}` 节点被改写

**错误**：

| 场景 | code | HTTP |
|------|------|------|
| 请求体不是合法 JSON | `bad_request` | 400 |
| 空 patch（四个字段都没传）| `bad_request` | 400 |
| `type` 不在允许列表中 | `bad_request` | 400 |
| `{name}` 不在 `llm.providers` 中 | `update_failed` | 400 |
| 新配置导致 Bifrost adapter 构建失败 | `update_failed` | 400 |
| 内存改成功但 yaml 写盘失败 | `persist_failed` | 500 |

---

## 客户端典型流程

### 切换主 provider

```js
// 1. 拿当前 chain
const { data } = await fetch('/api/v1/providers/fallback-chain').then(r => r.json());
// 2. 把目标 provider 挪到 chain[0]
const next = ['openrouter', ...data.chain.filter(n => n !== 'openrouter')];
// 3. PUT 新 chain，响应里直接拿新状态
const { data: updated } = await fetch('/api/v1/providers/fallback-chain', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ chain: next }),
}).then(r => r.json());
// 4. 用 updated 渲染 UI（chain + entries 全在里面）
```

### 轮换 api_key

```js
await fetch('/api/v1/providers/openai', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ api_key: 'sk-new-rotated-key' }),
}).then(r => r.json());
// 服务端立即用新 key dial 下一个请求；旧 key 不再缓存
```

### 实时监控健康状态

GET `/api/v1/providers/fallback-chain` 每 5 秒轮询 —— 看 `entries[*].state` 在 `healthy` / `tripped` / `ready_to_probe` 之间的迁移。

> 不要更高频率 —— 状态变化只在真实请求触发，更高频率得到的是同样数据。

---

## 语义说明

### 热生效与原子性

每次 mutation 的步骤（按顺序）：

1. **校验**：unknown provider / empty chain 等先拦下，**不动状态**
2. **构建新 adapter**（PATCH 时）/ **重排 chain**（PUT 时）：失败立即返回 `update_failed`，仍然**不动状态**
3. **原子 swap**：用 `atomic.Pointer` 把新 Failover 替换上线 —— 这一步是无锁、无 in-flight 请求受影响
4. **写 yaml**：以"先写临时文件 + rename"原子替换。`fsync` 由操作系统决定

In-flight 请求（步骤 3 之前已经派给老 dispatcher 的）**继续用老 chain 完成**；步骤 3 之后到达的新请求**全部走新 chain**。

### `persist_failed` 的含义

如果步骤 4 写 yaml 失败（磁盘满 / 权限），API 返回 500 `persist_failed`，但**内存中的 chain / provider 已经是新值**：

- 当前正在运行的服务端**用的是新值**
- 服务端**重启后**会从老 yaml 重建，**回到旧值**

操作员应当：

1. 解决磁盘问题
2. 重试 PUT / PATCH，让 yaml 写回成功
3. 或者：重启服务端（接受回退到旧值）

客户端建议把 `persist_failed` 当作"内存生效、磁盘失败"展示，给用户重试按钮。

### 单 provider 部署

服务端启动时如果 `llm.fallback_chain` 留空或只 1 项，**不挂载本 API**。所有 `/api/v1/providers*` 请求返回 404。这是有意的 —— 单 provider 部署没有"热切换"语义。

要启用本 API：在 `config_self.yaml` 里给 `fallback_chain` 至少 2 个 entry 并重启。

### 与 Models Registry 的关系

[Models Registry API](./models-registry-api.md) 是**只读**的"我支持哪些模型 + 它们的能力"; 本 API 是**读写**的"当前服务端配的是哪些 provider + chain"。两者完全独立 —— Models Registry 的 manifest 是嵌入式静态数据，跟 yaml 配置无关。

如果用户想 "把 `claude-opus-4-7` 切成主"，UI 应该：
1. 从 Models Registry 拿到 `provider=anthropic, model=claude-opus-4-7`
2. 调 PATCH `/api/v1/providers/anthropic` 把 model 改成 `claude-opus-4-7`
3. （可选）调 PUT `/api/v1/providers/fallback-chain` 把 `anthropic` 挪到 chain[0]

---

## 完整请求示例（curl）

```bash
# 列 providers
curl -sS http://localhost:8090/api/v1/providers

# 看当前 chain + health
curl -sS http://localhost:8090/api/v1/providers/fallback-chain

# 把 chain 反转
curl -sS -X PUT http://localhost:8090/api/v1/providers/fallback-chain \
  -H 'Content-Type: application/json' \
  -d '{"chain":["openai","anthropic"]}'

# 改 openai 的 model
curl -sS -X PATCH http://localhost:8090/api/v1/providers/openai \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-chat"}'

# 同时改 model + api_key
curl -sS -X PATCH http://localhost:8090/api/v1/providers/openai \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-chat","api_key":"sk-new"}'

# 切换后端协议（把 anthropic 换成 openai 后端 + 同步换 model）
curl -sS -X PATCH http://localhost:8090/api/v1/providers/claude-46 \
  -H 'Content-Type: application/json' \
  -d '{"type":"openai","model":"gpt-5","base_url":"https://api.openai.com"}'
```
