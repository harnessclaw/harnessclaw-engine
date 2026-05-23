// Package metatool implements meta_write — L3's task-completion declaration.
// Writes {taskDir}/meta.json exactly once via O_EXCL so accidental second
// calls are rejected (one task = one canonical summary).
package metatool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

const ToolName = "meta_write"

type MetaWriteTool struct {
	tool.BaseTool
	rootDir string
}

func NewMetaWriteTool(rootDir string) *MetaWriteTool {
	return &MetaWriteTool{rootDir: rootDir}
}

func (*MetaWriteTool) Name() string                  { return ToolName }
func (*MetaWriteTool) Description() string           { return description }
func (*MetaWriteTool) IsReadOnly() bool              { return false }
func (*MetaWriteTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }
func (*MetaWriteTool) IsEnabled() bool               { return true }
func (*MetaWriteTool) IsConcurrencySafe() bool       { return true }

func (*MetaWriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":  map[string]any{"type": "string", "enum": []string{"done", "failed"}},
			"summary": map[string]any{"type": "string", "description": "≤ 500 字。不要塞内容；只描述产物形态、要点、字数等。"},
			"outputs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "产物的绝对路径"},
						"type": map[string]any{"type": "string", "description": "可选；产物类型标签如 markdown/json/code"},
					},
					"required": []string{"path"},
				},
			},
			"consumed_inputs": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"status", "summary"},
	}
}

type inputOutput struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

type input struct {
	Status         string        `json:"status"`
	Summary        string        `json:"summary"`
	Outputs        []inputOutput `json:"outputs"`
	ConsumedInputs []string      `json:"consumed_inputs"`
}

func (t *MetaWriteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	// task_id / agent come from AgentScope (executor-injected), not LLM
	// input. The model has been observed to confuse task_id with session_id;
	// taking these from ctx eliminates that whole class of failure.
	scope, _ := tool.AgentScopeFromCtx(ctx)
	if scope.SessionRoot == "" {
		return errResult("SessionRoot missing in ctx — engine configuration error"), nil
	}
	if scope.TaskID == "" {
		return errResult("TaskID missing in ctx — engine configuration error"), nil
	}
	if scope.Agent == "" {
		return errResult("Agent missing in ctx — engine configuration error"), nil
	}

	// bytes are filled by os.Stat — the LLM can't know them accurately,
	// and asking it to guess produces zeros or hallucinations.
	outputs := make([]workspace.Output, 0, len(in.Outputs))
	for _, o := range in.Outputs {
		out := workspace.Output{Path: o.Path, Type: o.Type}
		if fi, err := os.Stat(o.Path); err == nil {
			out.Bytes = int(fi.Size())
		}
		outputs = append(outputs, out)
	}

	meta := &workspace.Meta{
		TaskID:         scope.TaskID,
		Agent:          scope.Agent,
		Status:         workspace.Status(in.Status),
		Summary:        in.Summary,
		Outputs:        outputs,
		ConsumedInputs: in.ConsumedInputs,
		EndedAt:        time.Now().UTC(),
	}
	if err := meta.Validate(); err != nil {
		return errResult(err.Error()), nil
	}

	relPath := filepath.Join("tasks", scope.TaskID, "meta.json")
	metaPath := filepath.Join(scope.SessionRoot, relPath)

	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return errResult("marshal: " + err.Error()), nil
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return errResult("mkdir: " + err.Error()), nil
	}
	f, err := os.OpenFile(metaPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return errResult(fmt.Sprintf("meta.json already exists for task %s — meta_write is single-shot", scope.TaskID)), nil
		}
		return errResult("open: " + err.Error()), nil
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return errResult("write: " + err.Error()), nil
	}
	return &types.ToolResult{Content: fmt.Sprintf("meta written; pass meta_path=%q to submit_task_result", relPath)}, nil
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: types.ToolErrorInvalidInput}
}

const description = `L3 task 结束时调用：写自己 task 目录的 meta.json，声明本次产物。

- 只允许调用一次（O_EXCL 兜底）。
- 入参只有 status / summary / outputs / consumed_inputs；task_id、agent、outputs[].bytes 由框架自动填，**不要在入参里写**。
- summary 必填、非空、≤ 500 字。**不要把内容粘进 summary**，只描述形态。
- outputs[].path 填产物的绝对路径。
- 写完后紧接着调 submit_task_result({task_id, meta_path}) 通知 L2 验收（task_id 在你看到的 task spawn 信息里）。`
