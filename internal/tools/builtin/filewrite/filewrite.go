// Package filewrite implements the FileWrite (Write) tool for creating/overwriting files.
package filewrite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

const toolName = "write"

// maxContentChars is the hard ceiling on a single write's content
// length. Set at 25000 to accommodate typical document drafts (long
// essays, full source files, structured config) while still preventing
// pathological dumps that would blow past the model's per-turn output
// cap (8192 tokens) when JSON-escaped. Above this we stop accepting
// the write and tell the LLM to split, because the alternative is
// watching it retry the same oversized payload until it exhausts
// max_turns. Observed: a freelancer trying to dump a 6 KB docx-js
// script into one write hit max_tokens truncation, then the next turn
// re-fed a partial JSON to bifrost and crashed the entire sub-agent
// stream.
//
// Trade-off at 25000: ASCII-heavy content (English code, plain text)
// fits comfortably under the 8k-token wall (~6k tokens with overhead).
// CJK-heavy content at the ceiling will exceed it (~12k tokens) and
// hit truncation — but the LLM has the schema's maxLength hint plus
// the truncation guards (defense lines 1+2) to recover.
const maxContentChars = 25000

// writeInput is the JSON structure the LLM sends to invoke the tool.
type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// FileWriteTool writes complete file contents.
type FileWriteTool struct {
	tool.BaseTool
	cfg config.ToolConfig
}

// New creates a FileWriteTool with the given config.
func New(cfg config.ToolConfig) *FileWriteTool {
	return &FileWriteTool{cfg: cfg}
}

func (t *FileWriteTool) Name() string        { return toolName }
func (t *FileWriteTool) Description() string { return fileWriteDescription }
func (t *FileWriteTool) IsReadOnly() bool                { return false }
func (t *FileWriteTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (t *FileWriteTool) IsEnabled() bool     { return t.cfg.Enabled }

func (t *FileWriteTool) InputSchema() map[string]any {
	// Build default working directory path with cross-platform support
	defaultDir := getDefaultWorkingDir()
	filePathDesc := fmt.Sprintf(
		"要写入文件的绝对路径（必须绝对，不能相对）。未指定具体位置时，可用 %s 作为默认工作目录。",
		defaultDir,
	)

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": filePathDesc,
			},
			"content": map[string]any{
				"type":      "string",
				"maxLength": maxContentChars,
				"description": fmt.Sprintf(
					"要写入文件的内容；**单次最长 %d 字符**。超出请分多次：先 write 一个最小骨架，"+
						"再用 edit 增量补全；或用 bash heredoc 分段追加每段 ≤ 1500 token。"+
						"一次塞超过这个长度会被 max_tokens 截断而失败。",
					maxContentChars,
				),
			},
		},
		"required": []string{"file_path", "content"},
	}
}

func (t *FileWriteTool) ValidateInput(input json.RawMessage) error {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if wi.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if !filepath.IsAbs(wi.FilePath) {
		return fmt.Errorf("file_path must be an absolute path")
	}
	if wi.Content == "" {
		return fmt.Errorf("content is required")
	}
	return nil
}

func (t *FileWriteTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var wi writeInput
	if err := json.Unmarshal(input, &wi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}

	if res := tool.EnforceWriteScope(ctx, wi.FilePath); res != nil {
		return res, nil
	}

	content := wi.Content
	if content == "" {
		return &types.ToolResult{Content: "content is required", IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}
	if n := len(content); n > maxContentChars {
		return &types.ToolResult{
			Content: fmt.Sprintf(
				"content too large: %d chars > limit %d. A single write must stay under %d "+
					"chars so the JSON arguments fit inside the model's 8192-token output "+
					"cap. Split the file: write a minimal skeleton first, then use multiple "+
					"`edit` calls to insert each section; or use `bash` heredoc to append "+
					"chunks. DO NOT retry the same payload — it will be truncated again.",
				n, maxContentChars, maxContentChars,
			),
			IsError:   true,
			ErrorType: types.ToolErrorInvalidInput,
			Metadata: map[string]any{
				"file_path":     wi.FilePath,
				"content_chars": n,
				"limit":         maxContentChars,
			},
		}, nil
	}

	// 父目录创建策略 —— 仅对 task_dir 白名单内的路径自动 MkdirAll。
	// 其他路径维持旧约束（不存在直接拒绝），避免 filewrite 沦为通用
	// "递归创建任意目录"工具：LLM 写错 / 注入恶意路径时可能在文件系统
	// 任意位置 mkdir，破坏沙箱。
	//
	// 白名单：ctx.AgentScope 提供 SessionRoot + TaskID，task_dir =
	// {SessionRoot}/tasks/{TaskID}。只有写入路径落在该子树下时才自动建。
	dir := filepath.Dir(wi.FilePath)
	if shouldAutoMkdir(ctx, wi.FilePath) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &types.ToolResult{
				Content:   fmt.Sprintf("create parent dir %s: %v", dir, err),
				IsError:   true,
				ErrorType: types.ToolErrorInternal,
			}, nil
		}
	} else if _, err := os.Stat(dir); os.IsNotExist(err) {
		return &types.ToolResult{
			Content: fmt.Sprintf(
				"directory %s does not exist; create it first (auto-mkdir is restricted to your task_dir under SessionRoot/tasks/<TaskID>)",
				dir,
			),
			IsError:   true,
			ErrorType: types.ToolErrorInvalidInput,
		}, nil
	}

	// Preserve existing permissions if file exists.
	perm := os.FileMode(0644)
	if info, err := os.Stat(wi.FilePath); err == nil {
		perm = info.Mode()
	}

	if err := os.WriteFile(wi.FilePath, []byte(content), perm); err != nil {
		return &types.ToolResult{Content: "error writing file: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	// render_hint deliberately omitted: promote is the sole Deliverable
	// source under the local-files-as-truth model. Surfacing file_info
	// here would let a sub-agent's intermediate scratch file land in the
	// user-visible Deliverable surface.
	return &types.ToolResult{
		Content: fmt.Sprintf("Successfully wrote to %s", wi.FilePath),
		Metadata: map[string]any{
			"file_path":     wi.FilePath,
			"language":      tool.ExtToLanguage(filepath.Ext(wi.FilePath)),
			"bytes_written": len(content),
		},
	}, nil
}


// shouldAutoMkdir 判断 filePath 是否落在当前 spawn 的 task_dir 白名单内 ——
// 仅在白名单内自动 MkdirAll，外部路径维持"父目录必须已存在"的旧约束，
// 防止 LLM 写错路径时在文件系统任意位置建目录。
//
// 白名单 = {AgentScope.SessionRoot}/tasks/{AgentScope.TaskID}（递归子树）。
// ctx 没注入 AgentScope（legacy / 测试路径）或缺关键字段时返回 false，
// 让 caller 走"父目录必须存在"的严格分支。
func shouldAutoMkdir(ctx context.Context, filePath string) bool {
	scope, ok := tool.AgentScopeFromCtx(ctx)
	if !ok || scope.SessionRoot == "" || scope.TaskID == "" {
		return false
	}
	taskDir := filepath.Join(scope.SessionRoot, "tasks", scope.TaskID)
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	// 用 Clean + 前缀比较 + 边界检查防 "/foo/tasks/t1-evil" 假命中 "/foo/tasks/t1"。
	cleanAbs := filepath.Clean(abs)
	cleanTaskDir := filepath.Clean(taskDir)
	if cleanAbs == cleanTaskDir {
		return true
	}
	prefix := cleanTaskDir + string(filepath.Separator)
	return len(cleanAbs) > len(prefix) && cleanAbs[:len(prefix)] == prefix
}

var fileWriteDescription = fmt.Sprintf(`把内容写入本地文件系统。

使用规范：
- 目标路径已存在文件时会被覆盖。
- file_path 必须是绝对路径，不能相对路径。
- **写入你自己的 task_dir 内时父目录会自动创建**（MkdirAll），不用预先 mkdir。
- **写入 task_dir 外的路径时父目录必须已存在**——这是沙箱保护，避免误写到任意位置。如果工作上下文有"task_dir"路径，把产物都写在它的子树下。
- content 单次最长 %d 字符。超出请分多次：先 write 骨架，再用 edit 增量补全；或用 bash heredoc 追加每段 ≤ 1500 token。一次塞太多会被 max_tokens 截断而失败。`, maxContentChars)

// getDefaultWorkingDir returns the expanded default working directory path
// for file operations, with cross-platform support.
func getDefaultWorkingDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to literal path if home dir cannot be determined
		return "~/.harnessclaw/files/"
	}
	return filepath.Join(homeDir, ".harnessclaw", "files")
}
