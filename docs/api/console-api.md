# Console API 参考文档

> **更新记录**
>
> | 日期 | 变更 |
> |------|------|
> | 2026-04-25 | 初版：7 个 Agent 管理端点 |
> | 2026-04-25 | **数据源变更**：SQLite 为唯一数据源，本地 YAML 不再自动加载。YAML 仅通过 Import/Export API 使用 |
> | 2026-04-25 | **source 简化**：去掉 `api` 和 `yaml:<dir>`，统一为 `builtin`（系统）和 `custom`（用户自定义） |

Console API 是独立于对话通道的管理接口，用于 Agent 定义的增删改查、导入导出等运维操作。

- **Base URL**: `http://localhost:8090`
- **路由前缀**: `/console/v1/`
- **Content-Type**: `application/json`（除导出接口外）
- **数据源**: SQLite（唯一），YAML 仅作为导入/导出格式
- **端口**: 独立于对话通道（WebSocket `:8081`），默认 `:8090`

### 数据来源说明

| source 值 | 说明 | 来源 |
|-----------|------|------|
| `builtin` | 系统内置 Agent（不可删除） | 启动时 `SyncBuiltins` 写入 |
| `custom` | 用户自定义 Agent（API 创建或 YAML 导入） | `POST /console/v1/agents` 或 `POST /console/v1/agents/import` |

---

## 统一响应格式

### 成功响应

单个资源：

```json
{
  "code": "OK",
  "data": { ... }
}
```

列表资源：

```json
{
  "code": "OK",
  "data": [ ... ],
  "total": 42
}
```

无内容（204 No Content）：无响应体。

### 错误响应

```json
{
  "code": "ERROR_CODE",
  "message": "错误描述"
}
```

### 错误码一览

| code | HTTP Status | 说明 |
|------|-------------|------|
| `OK` | 200/201 | 成功 |
| `NOT_FOUND` | 404 | 资源不存在 |
| `CONFLICT` | 409 | 名称冲突 |
| `FORBIDDEN` | 403 | 操作被禁止（如删除内置 Agent） |
| `BAD_REQUEST` | 400 | 请求参数错误 |
| `INTERNAL_ERROR` | 500 | 服务器内部错误 |

---

## AgentDefinition Schema

```json
{
  "name":             "string  (必填, 唯一标识名)",
  "display_name":     "string  (可选, 人类可读名称)",
  "description":      "string  (可选, Agent 能力描述)",
  "agent_type":       "string  (可选, 枚举: sync | async | teammate | coordinator | custom, 默认 sync)",
  "profile":          "string  (可选, 枚举: full | explore | plan)",
  "system_prompt":    "string  (可选, 自定义系统提示词)",
  "model":            "string  (可选, LLM 模型覆盖, 如 claude-opus-4-6)",
  "max_turns":        "int     (可选, 最大执行轮次, 默认 0 表示继承)",
  "auto_team":        "bool    (可选, 是否启用自动 Team 模式)",
  "tools":            "[string] (可选, 独立工具白名单)",
  "allowed_tools":    "[string] (可选, 工具白名单, 空则按 agent_type 默认过滤)",
  "disallowed_tools": "[string] (可选, 额外工具黑名单)",
  "skills":           "[string] (可选, 可用 Skill 白名单, 空则全部可用)",
  "sub_agents":       "[SubAgentDef] (可选, 预定义子 Agent 列表)",
  "source":           "string  (只读, 来源标记: builtin | custom)"
}
```

### SubAgentDef Schema

```json
{
  "name":       "string  (必填, 子 Agent 标识名)",
  "role":       "string  (可选, 角色描述)",
  "agent_type": "string  (可选, 枚举: sync | async | teammate | coordinator | custom)",
  "profile":    "string  (可选, 枚举: full | explore | plan)"
}
```

---

## Agent Management API

### 1. 创建 Agent

创建一个新的 Agent 定义。

**请求**

```
POST /console/v1/agents
Content-Type: application/json
```

```json
{
  "name": "my-researcher",
  "display_name": "Research Agent",
  "description": "专注于代码库探索和信息检索的 Agent",
  "agent_type": "sync",
  "profile": "explore",
  "model": "claude-sonnet-4-20250514",
  "max_turns": 10,
  "allowed_tools": ["Read", "Grep", "Glob", "WebSearch"],
  "skills": ["find-skills"],
  "auto_team": false
}
```

**成功响应**

```
HTTP/1.1 201 Created
Content-Type: application/json
```

```json
{
  "code": "OK",
  "data": {
    "name": "my-researcher",
    "display_name": "Research Agent",
    "description": "专注于代码库探索和信息检索的 Agent",
    "agent_type": "sync",
    "profile": "explore",
    "model": "claude-sonnet-4-20250514",
    "max_turns": 10,
    "allowed_tools": ["Read", "Grep", "Glob", "WebSearch"],
    "skills": ["find-skills"],
    "auto_team": false,
    "source": "custom"
  }
}
```

**错误响应**

名称冲突：

```
HTTP/1.1 409 Conflict
```

```json
{
  "code": "CONFLICT",
  "message": "agent 'my-researcher' already exists"
}
```

缺少必填字段：

```
HTTP/1.1 400 Bad Request
```

```json
{
  "code": "BAD_REQUEST",
  "message": "field 'name' is required"
}
```

---

### 2. 获取 Agent 列表

分页获取 Agent 定义列表，支持按 `agent_type` 和 `source` 过滤。

**请求**

```
GET /console/v1/agents?agent_type=sync&source=api&limit=20&offset=0
```

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `agent_type` | string | 否 | - | 按 Agent 类型过滤 |
| `source` | string | 否 | - | 按来源过滤：`builtin` 或 `custom` |
| `limit` | int | 否 | 20 | 每页数量，最大 100 |
| `offset` | int | 否 | 0 | 偏移量 |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "code": "OK",
  "data": [
    {
      "name": "general-purpose",
      "display_name": "General Purpose",
      "description": "General-purpose agent for complex, multi-step tasks",
      "agent_type": "sync",
      "profile": "full",
      "source": "builtin"
    },
    {
      "name": "my-researcher",
      "display_name": "Research Agent",
      "description": "专注于代码库探索和信息检索的 Agent",
      "agent_type": "sync",
      "profile": "explore",
      "source": "custom"
    }
  ],
  "total": 12
}
```

---

### 3. 获取单个 Agent

根据名称获取单个 Agent 定义的完整信息。

**请求**

```
GET /console/v1/agents/{name}
```

| 参数 | 位置 | 类型 | 说明 |
|------|------|------|------|
| `name` | path | string | Agent 唯一标识名 |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "code": "OK",
  "data": {
    "name": "coordinator",
    "display_name": "Coordinator",
    "description": "Team leader that delegates work to teammates and synthesizes results",
    "agent_type": "coordinator",
    "profile": "plan",
    "system_prompt": "...",
    "allowed_tools": ["Agent", "TaskStop", "SendMessage", "SyntheticOutput"],
    "max_turns": 50,
    "source": "builtin"
  }
}
```

**错误响应**

```
HTTP/1.1 404 Not Found
```

```json
{
  "code": "NOT_FOUND",
  "message": "agent 'unknown-agent' not found"
}
```

---

### 4. 更新 Agent

更新指定 Agent 的定义字段。仅传入需要修改的字段（部分更新）。

**请求**

```
PUT /console/v1/agents/{name}
Content-Type: application/json
```

```json
{
  "description": "更新后的描述",
  "max_turns": 15,
  "allowed_tools": ["Read", "Grep", "Glob", "Bash", "WebSearch"]
}
```

| 参数 | 位置 | 类型 | 说明 |
|------|------|------|------|
| `name` | path | string | Agent 唯一标识名 |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "code": "OK",
  "data": {
    "name": "my-researcher",
    "display_name": "Research Agent",
    "description": "更新后的描述",
    "agent_type": "sync",
    "profile": "explore",
    "model": "claude-sonnet-4-20250514",
    "max_turns": 15,
    "allowed_tools": ["Read", "Grep", "Glob", "Bash", "WebSearch"],
    "skills": ["find-skills"],
    "auto_team": false,
    "source": "custom"
  }
}
```

**错误响应**

Agent 不存在：

```
HTTP/1.1 404 Not Found
```

```json
{
  "code": "NOT_FOUND",
  "message": "agent 'my-researcher' not found"
}
```

内置 Agent 受保护字段不可修改：

```
HTTP/1.1 403 Forbidden
```

```json
{
  "code": "FORBIDDEN",
  "message": "cannot change 'name' or 'agent_type' of builtin agent 'coordinator'"
}
```

---

### 5. 删除 Agent

删除指定的 Agent 定义。内置 Agent 不可删除。

**请求**

```
DELETE /console/v1/agents/{name}
```

| 参数 | 位置 | 类型 | 说明 |
|------|------|------|------|
| `name` | path | string | Agent 唯一标识名 |

**成功响应**

```
HTTP/1.1 204 No Content
```

（无响应体）

**错误响应**

内置 Agent 不可删除：

```
HTTP/1.1 403 Forbidden
```

```json
{
  "code": "FORBIDDEN",
  "message": "cannot delete builtin agent 'general-purpose'"
}
```

Agent 不存在：

```
HTTP/1.1 404 Not Found
```

```json
{
  "code": "NOT_FOUND",
  "message": "agent 'unknown-agent' not found"
}
```

---

### 6. 从 YAML 目录导入

批量从指定目录导入 YAML 格式的 Agent 定义。已存在的 Agent 将被跳过。

**请求**

```
POST /console/v1/agents/import
Content-Type: application/json
```

```json
{
  "dir": ".harnessclaw/agents"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `dir` | string | 是 | YAML 文件所在目录路径（相对于工作目录或绝对路径） |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "code": "OK",
  "data": {
    "imported": 5,
    "skipped": 2,
    "errors": [
      {
        "file": "broken-agent.yaml",
        "error": "yaml: unmarshal error: line 3: cannot unmarshal"
      }
    ]
  }
}
```

**错误响应**

目录不存在：

```
HTTP/1.1 400 Bad Request
```

```json
{
  "code": "BAD_REQUEST",
  "message": "directory '.harnessclaw/agents' does not exist"
}
```

---

### 7. 导出为 YAML

将单个 Agent 定义导出为 YAML 格式。

**请求**

```
GET /console/v1/agents/{name}/export
```

| 参数 | 位置 | 类型 | 说明 |
|------|------|------|------|
| `name` | path | string | Agent 唯一标识名 |

**成功响应**

```
HTTP/1.1 200 OK
Content-Type: application/x-yaml
Content-Disposition: attachment; filename="my-researcher.yaml"
```

```yaml
name: my-researcher
display_name: Research Agent
description: 专注于代码库探索和信息检索的 Agent
agent_type: sync
profile: explore
model: claude-sonnet-4-20250514
max_turns: 10
allowed_tools:
  - Read
  - Grep
  - Glob
  - WebSearch
skills:
  - find-skills
```

**错误响应**

```
HTTP/1.1 404 Not Found
Content-Type: application/json
```

```json
{
  "code": "NOT_FOUND",
  "message": "agent 'unknown-agent' not found"
}
```
