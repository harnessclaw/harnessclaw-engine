// Package metatool implements MetaWrite — L3's task-completion declaration.
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

const ToolName = "MetaWrite"

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
			"task_id": map[string]any{"type": "string"},
			"agent":   map[string]any{"type": "string"},
			"status":  map[string]any{"type": "string", "enum": []string{"done", "failed"}},
			"summary": map[string]any{"type": "string", "description": "≤ 500 字。不要塞内容；只描述产物形态、要点、字数等。"},
			"outputs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":  map[string]any{"type": "string", "description": "产物的绝对路径"},
						"type":  map[string]any{"type": "string"},
						"bytes": map[string]any{"type": "integer"},
					},
					"required": []string{"path"},
				},
			},
			"consumed_inputs": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"task_id", "agent", "status", "summary"},
	}
}

type input struct {
	TaskID         string             `json:"task_id"`
	Agent          string             `json:"agent"`
	Status         string             `json:"status"`
	Summary        string             `json:"summary"`
	Outputs        []workspace.Output `json:"outputs"`
	ConsumedInputs []string           `json:"consumed_inputs"`
}

func (t *MetaWriteTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	meta := &workspace.Meta{
		TaskID:         in.TaskID,
		Agent:          in.Agent,
		Status:         workspace.Status(in.Status),
		Summary:        in.Summary,
		Outputs:        in.Outputs,
		ConsumedInputs: in.ConsumedInputs,
		EndedAt:        time.Now().UTC(),
	}
	if err := meta.Validate(); err != nil {
		return errResult(err.Error()), nil
	}

	// Derive the meta.json path from AgentScope.SessionRoot injected by the
	// executor — avoids requiring the LLM to pass a session_id it might
	// omit or hallucinate.
	scope, _ := tool.AgentScopeFromCtx(ctx)
	if scope.SessionRoot == "" {
		return errResult("SessionRoot missing in ctx — engine configuration error"), nil
	}
	relPath := filepath.Join("tasks", in.TaskID, "meta.json")
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
			return errResult(fmt.Sprintf("meta.json already exists for task %s — MetaWrite is single-shot", in.TaskID)), nil
		}
		return errResult("open: " + err.Error()), nil
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return errResult("write: " + err.Error()), nil
	}
	return &types.ToolResult{Content: fmt.Sprintf("meta written; pass meta_path=%q to SubmitTaskResult", relPath)}, nil
}

func errResult(msg string) *types.ToolResult {
	return &types.ToolResult{Content: msg, IsError: true, ErrorType: types.ToolErrorInvalidInput}
}

const description = `L3 task 结束时调用：写自己 task 目录的 meta.json，声明本次产物。

- 只允许调用一次（O_EXCL 兜底）。
- summary 必填、非空、≤ 500 字。**不要把内容粘进 summary**，只描述形态。
- outputs[].path 填产物的绝对路径。
- 写完后通常紧接着调 SubmitTool({task_id, meta_path}) 通知 L2 验收。`
