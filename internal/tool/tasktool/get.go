package tasktool

import (
	"context"
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type GetTool struct {
	tool.BaseTool
	store   task.Store
	scopeID string
}

func NewGet(store task.Store, scopeID string) *GetTool {
	return &GetTool{store: store, scopeID: scopeID}
}

func (t *GetTool) Name() string             { return "TaskGet" }
func (t *GetTool) Description() string       { return "按 ID 取一个任务。" }
func (t *GetTool) IsReadOnly() bool          { return true }
func (t *GetTool) IsConcurrencySafe() bool   { return true }

func (t *GetTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"taskId": map[string]any{"type": "string", "description": "要查询的任务 ID。"},
		},
		"required": []string{"taskId"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Task retrieval is auto-allowed — no user confirmation needed.
func (t *GetTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *GetTool) ValidateInput(input json.RawMessage) error {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return err
	}
	if p.TaskID == "" {
		return fmt.Errorf("taskId is required")
	}
	return nil
}

func (t *GetTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	tk, err := t.store.Get(ctx, t.scopeID, p.TaskID)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	out, _ := json.Marshal(tk)
	return &types.ToolResult{
		Content:  string(out),
		Metadata: map[string]any{"render_hint": "task"},
	}, nil
}
