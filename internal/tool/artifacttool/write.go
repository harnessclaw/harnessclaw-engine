// Package artifacttool implements the LLM-facing ArtifactRead and
// ArtifactWrite tools. These are the only entry points agents use to
// persist or retrieve cross-agent shared data — see doc §5.
//
// Tool names use PascalCase (no dot separator) because OpenAI's tool-name
// validator rejects `^[a-zA-Z0-9_-]+$`-violating names like "artifact.read".
//
// The store itself is injected into the tool execution context by the
// engine; tools never touch storage backends directly. This keeps the
// tool layer storage-agnostic and avoids a tool→artifact→tool import
// cycle (we import artifact one-way here, not the other direction).
package artifacttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// WriteToolName is the public LLM-facing name. PascalCase mirrors the
// rest of the tool palette (Bash/WebFetch/AskUserQuestion) and stays
// inside OpenAI's tool-name regex.
const WriteToolName = "ArtifactWrite"

// writeInput is the parsed payload. The fields map to SaveInput; producer
// identity is supplied by the engine via context, never by the LLM.
type writeInput struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	MIMEType    string          `json:"mime_type,omitempty"`
	Encoding    string          `json:"encoding,omitempty"`
	Content     string          `json:"content"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	// ParentArtifactID requests a versioned write derived from an existing
	// artifact. Optional. The store auto-bumps Version.
	ParentArtifactID string `json:"parent_artifact_id,omitempty"`
	// TTLSeconds overrides the default TTL. Optional.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
	// Scope optionally widens visibility from the default "trace" scope.
	Scope string `json:"scope,omitempty"`
}

// WriteTool persists data and returns a Ref the LLM can pass to other agents.
type WriteTool struct {
	tool.BaseTool
}

// NewWriteTool returns the registered tool instance.
func NewWriteTool() *WriteTool {
	return &WriteTool{}
}

func (*WriteTool) Name() string             { return WriteToolName }
func (*WriteTool) Description() string      { return writeDescription }
func (*WriteTool) IsReadOnly() bool                  { return false }
func (*WriteTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*WriteTool) IsEnabled() bool          { return true }
func (*WriteTool) IsConcurrencySafe() bool  { return true } // appends to an immutable store

// InputSchema is the LLM-facing JSON schema. Order of properties is the
// order the LLM tends to follow when filling them in, so put the
// load-bearing ones first.
func (*WriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"structured", "file", "blob"},
				"description": "artifact 类型。structured=带 schema 的 JSON 数据；file=文本内容（markdown/csv/源码）；blob=二进制（只保存，不允许 LLM 全量读取）。",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "可读的短文件名（例如 'sales-2024.md'）。用户界面卡片和列表里展示。",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "一行话讲清这是什么——帮下游 agent 决定要不要读。",
			},
			"mime_type": map[string]any{
				"type":        "string",
				"description": "MIME 类型（如 'text/markdown' / 'application/json' / 'text/csv'）。",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "artifact 的实际内容（内联）。二进制内容请 base64 编码并设 encoding='base64'。",
			},
			"schema": map[string]any{
				"type":        "object",
				"description": "可选 JSON schema，用于描述 structured 数据的形态（如表格列类型）。下游消费者用它正确解析。",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "自由格式标签，便于后续列表/检索。",
			},
			"parent_artifact_id": map[string]any{
				"type":        "string",
				"description": "传入时，本次写入会变成那个 artifact 的新版本。store 自动升版本号，原版保留。",
			},
			"ttl_seconds": map[string]any{
				"type":        "integer",
				"description": "存活时间覆盖，默认 3600（1 小时）。只有需要跨当前 trace 长期保留时才设更长。",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"trace", "session", "user"},
				"description": "可见范围。'trace'（默认，最安全）：只有本次用户请求能读。'session'：后续轮次可读。'user'：需要用户明确「留存」意图。",
			},
		},
		"required": []string{"type", "content"},
	}
}

// ValidateInput rejects payloads the store would reject anyway, with a
// clearer error message so the LLM knows how to fix its call.
//
// Error strings include hints because LLMs commonly fudge the `type`
// field (e.g. "markdown" / "csv" / "json"). A bare "must be one of
// structured|file|blob" leaves the model guessing on retry; the hint
// turns each rejection into a self-correcting cycle.
func (*WriteTool) ValidateInput(raw json.RawMessage) error {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Type) == "" {
		return fmt.Errorf("type is required (one of structured|file|blob). " +
			"Use 'file' for any text (markdown/csv/source/log/json text), " +
			"'structured' for JSON-shaped data with a schema, " +
			"'blob' for binary content")
	}
	switch artifact.Type(in.Type) {
	case artifact.TypeStructured, artifact.TypeFile, artifact.TypeBlob:
	default:
		return fmt.Errorf(
			"type %q is not allowed; use one of: structured, file, blob. "+
				"Hints: 'markdown'/'csv'/'text'/'source'/'log' → use 'file'; "+
				"'json'/'table'/'list' → use 'structured'; "+
				"'image'/'pdf'/'audio'/'binary' → use 'blob'",
			in.Type,
		)
	}
	if in.Content == "" {
		return fmt.Errorf("content is required — pass the actual data as a non-empty string. " +
			"For structured types, JSON-encode the value before passing")
	}
	if in.Scope != "" {
		switch artifact.Scope(in.Scope) {
		case artifact.ScopeTrace, artifact.ScopeSession, artifact.ScopeUser:
		default:
			return fmt.Errorf("scope must be trace|session|user, got %q", in.Scope)
		}
	}
	if in.TTLSeconds < 0 {
		return fmt.Errorf("ttl_seconds must be non-negative")
	}
	return nil
}

// Execute persists the artifact and returns a Ref-shaped JSON the LLM can
// pass downstream.
func (*WriteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	store, ok := getStore(ctx)
	if !ok {
		return errResult("artifact store not configured for this engine"), nil
	}
	producer, _ := tool.GetArtifactProducer(ctx)

	saveIn := &artifact.SaveInput{
		Type:             artifact.Type(in.Type),
		MIMEType:         in.MIMEType,
		Encoding:         in.Encoding,
		Name:             in.Name,
		Description:      in.Description,
		Content:          in.Content,
		Schema:           in.Schema,
		Tags:             in.Tags,
		ParentArtifactID: in.ParentArtifactID,
		Producer: artifact.Producer{
			AgentID:    producer.AgentID,
			AgentRunID: producer.AgentRunID,
			TaskID:     producer.TaskID,
		},
		TraceID:   producer.TraceID,
		SessionID: producer.SessionID,
	}
	if in.Scope != "" {
		saveIn.Access.Scope = artifact.Scope(in.Scope)
	}
	if in.TTLSeconds > 0 {
		saveIn.TTL = time.Duration(in.TTLSeconds) * time.Second
	}

	a, err := store.Save(ctx, saveIn)
	if err != nil {
		return errResult("save artifact: " + err.Error()), nil
	}

	resp := struct {
		ArtifactID string         `json:"artifact_id"`
		URI        string         `json:"uri"`
		Size       int            `json:"size_bytes"`
		Preview    string         `json:"preview,omitempty"`
		Version    int            `json:"version"`
		Ref        artifact.Ref   `json:"ref"`
	}{
		ArtifactID: a.ID,
		URI:        a.URI,
		Size:       a.Size,
		Preview:    a.Preview,
		Version:    a.Version,
		Ref:        a.ToRef(),
	}
	body, _ := json.Marshal(resp)

	// Metadata mirrors the wire-shape ArtifactRef (pkg/types/artifact_ref.go):
	// the executor reads these fields to populate EngineEvent.Artifacts on
	// tool_end without parsing the JSON Content. Keep field names in sync;
	// renaming any here breaks the executor's extraction and the front-end
	// stops seeing produced artifacts.
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint":  "artifact",
			"artifact_id":  a.ID,
			"name":         a.Name,
			"type":         string(a.Type),
			"mime_type":    a.MIMEType,
			"size":         a.Size,
			"description":  a.Description,
			"preview_text": a.Preview,
			"uri":          a.URI,
		},
	}, nil
}

const writeDescription = `把数据持久化为 artifact，返回一个 ID 供其他 agent 引用。

何时使用：
- 工具产出需要被另一个 agent 消费的数据（不要把内容粘回 prompt——存进来传 ID）。
- 你生成了报告/表格/文件，用户想保留下来访问。
- 数据足够大，反复粘贴会浪费 token。

不要用于：
- 只有你自己下一轮会消费的临时中间值——直接写在 prompt 里。
- 极小的常量（一个数字、一个是/否答案）。

artifact_id 由 store 分配，绝对不要自己编。每次都要写清楚 description，让下游 agent 能判断要不要 read。`
