package tasktool

import (
	"context"
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type CreateTool struct {
	tool.BaseTool
	store   task.Store
	scopeID string
}

func NewCreate(store task.Store, scopeID string) *CreateTool {
	return &CreateTool{store: store, scopeID: scopeID}
}

func (t *CreateTool) Name() string             { return "TaskCreate" }
func (t *CreateTool) Description() string       { return "向任务列表中添加一个新任务。" }
func (t *CreateTool) IsReadOnly() bool          { return false }
func (t *CreateTool) IsConcurrencySafe() bool   { return true }

func (t *CreateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subject":     map[string]any{"type": "string", "description": "简短的任务标题。"},
			"description": map[string]any{"type": "string", "description": "详细描述。"},
			"activeForm":  map[string]any{"type": "string", "description": "进度条用的进行时短语（例如 \"正在分析...\"）。"},
		},
		"required": []string{"subject", "description"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Task creation is auto-allowed — no user confirmation needed.
func (t *CreateTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *CreateTool) ValidateInput(input json.RawMessage) error {
	var p struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return err
	}
	if p.Subject == "" {
		return fmt.Errorf("subject is required")
	}
	if p.Description == "" {
		return fmt.Errorf("description is required")
	}
	return nil
}

func (t *CreateTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		Subject     string `json:"subject"`
		Description string `json:"description"`
		ActiveForm  string `json:"activeForm"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	newTask := &task.Task{
		Subject:     p.Subject,
		Description: p.Description,
		ActiveForm:  p.ActiveForm,
		ScopeID:     t.scopeID,
	}

	created, err := t.store.Create(ctx, newTask)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	out, _ := json.Marshal(created)
	return &types.ToolResult{
		Content:  string(out),
		Metadata: map[string]any{"render_hint": "task"},
	}, nil
}
