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
	"mime"
	"path/filepath"
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
	Content     string          `json:"content,omitempty"`
	// blobBytes is internal — populated by resolveSourcePath after reading
	// the source file, then handed off to artifact.SaveInput.BlobBytes.
	// Not exposed in the LLM-facing JSON schema; it's purely a carrier
	// between the path-validation step and the store-call step.
	blobBytes []byte `json:"-"`
	// SourcePath is the alternative to Content for binaries: the server
	// reads the file (subject to an allow-list, see source_path.go) and
	// fills Content / Encoding / MIMEType itself. This bypasses the LLM
	// having to base64-copy binary data through tool_call JSON, which it
	// historically corrupts on any non-trivial payload.
	//
	// When both Content and SourcePath are set, the call is rejected at
	// ValidateInput — we don't want a future LLM to "helpfully" set both
	// and have an ambiguous winner.
	SourcePath       string          `json:"source_path,omitempty"`
	Schema           json.RawMessage `json:"schema,omitempty"`
	Tags             []string        `json:"tags,omitempty"`
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
	// allowedReadDirs is the canonicalised allow-list for source_path.
	// Empty disables source_path entirely (legacy behaviour: content-only
	// writes still work).
	allowedReadDirs []string
}

// NewWriteTool returns the registered tool instance. Pass cfg.Workspace
// and cfg.Skills.Dirs (or whatever roots are safe for the server to read
// on the LLM's behalf) as allowedReadDirs. Pass nil/empty to keep the
// legacy content-only behaviour — source_path will be rejected with a
// "not configured" error in that case.
func NewWriteTool(allowedReadDirs ...string) *WriteTool {
	return &WriteTool{
		allowedReadDirs: canonicaliseAllowedDirs(allowedReadDirs),
	}
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
				"description": "artifact 的实际内容（内联）。**仅用于文本** —— markdown / csv / 源码 / JSON 等。二进制（docx / pdf / xlsx / image）请改用 source_path 让服务端读文件，不要在 LLM 里 base64 复述。",
			},
			"source_path": map[string]any{
				"type": "string",
				"description": "可选。二进制 artifact 的源文件**绝对路径**——服务端直接读文件并自动 base64 编码存储，避免 LLM 复述 base64 时损坏字节。" +
					"\n\n使用场景：你在 Bash 里用脚本生成了 docx / pdf / xlsx / image 文件，想把它持久化为 artifact。" +
					"\n\n限制：" +
					"\n- 必须是绝对路径" +
					"\n- 必须在服务端允许的目录下（workspace / skills dirs）" +
					"\n- 单文件 ≤ 50MB" +
					"\n- 不可与 content 同时给（互斥）" +
					"\n\n示例：source_path=\"/Users/xxx/.harnessclaw/workspace/report.docx\"，type=\"blob\"，mime_type 与 name 可省（服务端会从扩展名推断）。",
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
		// content is no longer strictly required at the schema layer
		// because source_path is a valid alternative. ValidateInput
		// enforces "exactly one of content / source_path" with a clearer
		// error message than json-schema's oneOf could provide.
		"required": []string{"type"},
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
	// Either content (inline) or source_path (server-read) must be set,
	// but never both. Without this guard a future caller could specify
	// both and we'd silently pick a winner.
	if in.Content == "" && in.SourcePath == "" {
		return fmt.Errorf("either content or source_path is required. " +
			"For text artifacts pass content (markdown / json / source). " +
			"For binary artifacts (docx / pdf / image) pass source_path with the absolute path " +
			"to a file the server can read — do not base64-copy binary data into content.")
	}
	if in.Content != "" && in.SourcePath != "" {
		return fmt.Errorf("content and source_path are mutually exclusive — set exactly one")
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
func (w *WriteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResultTyped("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}

	// source_path branch: read the file server-side and synthesise the
	// Content / Encoding / MIMEType / Name fields the rest of the
	// pipeline expects. Doing this BEFORE getStore so the path-validation
	// error message reaches the LLM even if the store isn't wired.
	if in.SourcePath != "" {
		if err := w.resolveSourcePath(&in); err != nil {
			return errResultTyped(err.Error(), types.ToolErrorInvalidInput), nil
		}
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
		BlobBytes:        in.blobBytes, // empty unless source_path branch populated it
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

// resolveSourcePath reads in.SourcePath under the WriteTool's allow-list
// and fills in.blobBytes / in.Content / in.MIMEType / in.Name as needed.
// After this returns nil, downstream code (SaveInput build, store.Save)
// handles the bytes via either SaveInput.BlobBytes (binary, server keeps
// the file external) or SaveInput.Content (text, stays inline).
//
// Behaviour:
//   - Reads bytes via readFromAllowedPath (handles abs/symlink/allow-list/size).
//   - For type="blob": bytes flow through SaveInput.BlobBytes — the
//     SQLiteStore writes them to its companion blob directory and the
//     metadata row only carries a path reference. No base64 inflation
//     of the DB. Get(...) hydrates back to base64-encoded Content
//     transparently when callers read.
//   - For type="file" / "structured": bytes go inline as UTF-8 content
//     (text was the original design; this branch is unchanged).
//   - Name defaults to filepath.Base(SourcePath) if the LLM didn't supply one.
//   - MIME defaults to extension-based detection if not supplied.
func (w *WriteTool) resolveSourcePath(in *writeInput) error {
	data, _, err := readFromAllowedPath(in.SourcePath, w.allowedReadDirs)
	if err != nil {
		return err
	}

	// Default name to the file's base name. The LLM almost always omits
	// this because it's redundant with the path, but the artifact store
	// requires a non-empty name for the UI.
	if in.Name == "" {
		in.Name = filepath.Base(in.SourcePath)
	}

	// MIME default from file extension.
	if in.MIMEType == "" {
		if ct := mime.TypeByExtension(filepath.Ext(in.SourcePath)); ct != "" {
			in.MIMEType = ct
		}
	}

	// Storage route:
	//   - type=blob → hand bytes to SaveInput.BlobBytes (external file)
	//   - type=file / structured → inline UTF-8 as before
	switch {
	case in.Type == string(artifact.TypeBlob):
		in.blobBytes = data
		// Encoding stays empty here — the wire encoding is meaningful
		// only for downstream Get callers, who'll see "base64" after
		// hydration. Recording it now would be misleading because the
		// DB row's content column is empty.
	default:
		in.Content = string(data)
	}
	return nil
}

const writeDescription = `把数据持久化为 artifact，返回一个 ID 供其他 agent 引用。

何时使用：
- 工具产出需要被另一个 agent 消费的数据（不要把内容粘回 prompt——存进来传 ID）。
- 你生成了报告/表格/文件，用户想保留下来访问。
- 数据足够大，反复粘贴会浪费 token。

不要用于：
- 只有你自己下一轮会消费的临时中间值——直接写在 prompt 里。
- 极小的常量（一个数字、一个是/否答案）。

## 内容来源：两种模式，二选一

**A. 文本（推荐 ≤200 KB）** — 直接传 ` + "`content`" + ` 字段：
- markdown / json / 源码 / csv / 日志 等文本内容
- 类型设 ` + "`type=\"file\"`" + ` 或 ` + "`type=\"structured\"`" + `

**B. 二进制 / 大文件 — 必须用 ` + "`source_path`" + `**：
- 任何 .docx / .pdf / .xlsx / .pptx / .png / .jpg / .zip / 编译产物 等二进制
- 任何 > 200 KB 的文件，无论是不是文本
- 调用形式：` + "`ArtifactWrite(type=\"blob\", source_path=\"/绝对路径\", description=\"...\")`" + `
- 服务端读文件后自动做 base64 编码 + MIME 检测 + 字节级完整性
- 路径必须在 server 允许的目录（workspace / skills dirs）下

**⚠️ 绝对禁止给二进制走 A 路径**：
不要 ` + "`cat file.docx | base64`" + ` 把字节当字符串塞进 ` + "`content`" + `。LLM 在长 base64 串里
会插字符 / 漏字符 / 把 ` + "`+/`" + ` 当 markdown，下游解码后字节偏移错乱，docx/pdf 打开乱码。
这是已知失败模式——任何二进制 artifact 必须走 ` + "`source_path`" + `。

如果你的二进制还没在磁盘上：先用 Bash / 脚本把它写到 ` + "`~/.harnessclaw/workspace/`" + ` 下的
某个文件，再用 source_path 引用。**先落盘、后引用**，不要试图把生成和编码合并到一步。

## 其他约定

artifact_id 由 store 分配，绝对不要自己编。每次都要写清楚 description，让下游 agent 能判断要不要 read。`
