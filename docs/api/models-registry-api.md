# Model Registry API 参考文档

> **更新记录**
>
> | 日期 | 变更 |
> |------|------|
> | 2026-05-13 | 初版：`GET /api/v1/models` + `GET /api/v1/models/{provider}/{model_id}` |

Model Registry API 暴露每个支持的 LLM 模型和厂商的能力、限制、默认参数和厂商私有 quirks。客户端用它来：

- 渲染模型选择器（按厂商分组，显示模型 display_name + family）
- 根据 `supports.vision` / `audio_input` 等 flag 显示/隐藏功能按钮
- 根据 `limits.context_window` 限制对话长度
- 用 `defaults.*` 预填 UI 控件（temperature/top_p）

定价不在 schema 里 —— 客户端按 model 名查自己的定价表算钱。

- **Base URL**: `http://localhost:8090`（Console 管理端口）
- **路由前缀**: `/api/v1/models`
- **Content-Type**: `application/json`
- **数据来源**: 服务端启动时加载嵌入的 default-manifest.yaml

---

## 错误码

| error | HTTP Status | 说明 |
|-------|-------------|------|
| `model_not_found` | 404 | 模型 key 在 manifest 中不存在 |
| `bad_request` | 400 | 路径格式不正确 |
| `method_not_allowed` | 405 | 仅支持 GET |

---

## 端点

### 1. 列出所有模型

`GET /api/v1/models`

**响应**（200 OK）：

```json
{
  "data": [
    {
      "id": "anthropic/claude-opus-4-7",
      "provider": "anthropic",
      "model_id": "claude-opus-4-7",
      "display_name": "Claude Opus 4.7",
      "family": "claude-opus",
      "generation": "4.7",
      "knowledge_cutoff": "2026-01",
      "modalities": { "input": ["text", "image", "pdf"], "output": ["text"] },
      "supports": {
        "vision": true,
        "pdf_input": true,
        "streaming": true,
        "function_calling": true,
        "computer_use": true,
        "reasoning": true,
        "reasoning_can_disable": true,
        "prompt_caching": true,
        "explicit_cache_control": true
      },
      "limits": {
        "context_window": 1000000,
        "max_input_tokens": 872000,
        "max_output_tokens": 128000
      },
      "defaults": { "temperature": 1.0, "top_p": 1.0, "max_output_tokens_default": 8192 }
    },
    {
      "id": "deepseek/deepseek-v4-flash",
      "provider": "deepseek",
      "...": "see single-model example below"
    }
  ]
}
```

排序：按 `id` 字典序，前端可按 `provider` / `family` 再分组。

### 2. 查询单个模型

`GET /api/v1/models/{provider}/{model_id}`

例如 `GET /api/v1/models/deepseek/deepseek-v4-flash`：

**响应**（200 OK）：

```json
{
  "id": "deepseek/deepseek-v4-flash",
  "provider": "deepseek",
  "model_id": "deepseek-v4-flash",
  "display_name": "DeepSeek V4 Flash",
  "family": "deepseek-v4",
  "generation": "v4-flash",
  "modalities": { "input": ["text"], "output": ["text"] },
  "supports": {
    "streaming": true,
    "function_calling": true,
    "parallel_function_calling": true,
    "tool_choice": true,
    "reasoning": true,
    "reasoning_can_disable": true,
    "prompt_caching": true
  },
  "limits": {
    "context_window": 1000000,
    "max_input_tokens": 616000,
    "max_output_tokens": 384000
  },
  "defaults": { "temperature": 0.7, "top_p": 1.0, "max_output_tokens_default": 8192 }
}
```

**404**：

```json
{ "error": "model_not_found" }
```

---

## 字段语义

### `supports.*` 关键 flags

| 字段 | 含义 | UI 应用 |
|------|------|---------|
| `vision` | 接受 image_url 输入 | 显示上传图片按钮 |
| `pdf_input` | 接受 PDF 文件 | 显示上传 PDF |
| `audio_input` / `audio_output` | 音频输入/输出 | 麦克风按钮 |
| `video_input` | 视频输入（Gemini / Kimi） | 视频上传 |
| `function_calling` | 支持工具调用 | 显示工具开关 |
| `reasoning` | 支持思维链 | 显示 thinking 开关 |
| `reasoning_can_disable` | 可关闭思维链 | thinking 开关是否可选 |
| `reasoning_effort_levels` | 思维深度档位（OpenAI o-series） | 档位选择器（"low/medium/high"） |
| `web_search` | 模型自带联网能力 | 显示联网开关 |
| `prompt_caching` | 服务端支持 prompt cache | 显示「cache 节省」指标 |
| `explicit_cache_control` | 需客户端显式打 cache_control（Anthropic） | 内部使用，UI 一般无需关心 |

### `limits.*`

- `context_window`: 含输入 + 输出的最大上下文（tokens）
- `max_input_tokens`: 单次输入上限
- `max_output_tokens`: 单次最大输出
- `max_reasoning_tokens`: 思考预算上限（null = 无限制 / 不适用）

### `modalities.input` / `modalities.output`

数组，可能值：`text` | `image` | `audio` | `video` | `pdf` | `file`。

---

## 不暴露的字段

Manifest 中 `providers.*.quirks` 与 `providers.*.auth` 属于**服务端实现细节**，不通过 HTTP 暴露给客户端（避免泄露 API key header 或 wire 协议细节）。客户端只看 `models.*`。

将来若需暴露 provider display_name（按厂商分组用），可加 `GET /api/v1/models/providers` 端点，仅返回 `display_name + family + region`。本期不实现。
