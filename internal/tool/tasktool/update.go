package tasktool

import (
	"context"
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type UpdateTool struct {
	tool.BaseTool
	store   task.Store
	scopeID string
}

func NewUpdate(store task.Store, scopeID string) *UpdateTool {
	return &UpdateTool{store: store, scopeID: scopeID}
}

func (t *UpdateTool) Name() string             { return "TaskUpdate" }
func (t *UpdateTool) Description() string       { return "更新任务的 status / owner 或其他字段。" }
func (t *UpdateTool) IsReadOnly() bool          { return false }
func (t *UpdateTool) IsConcurrencySafe() bool   { return true }

func (t *UpdateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"taskId":       map[string]any{"type": "string", "description": "要更新的任务 ID。"},
			"status":       map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "deleted"}},
			"subject":      map[string]any{"type": "string"},
			"description":  map[string]any{"type": "string"},
			"activeForm":   map[string]any{"type": "string"},
			"owner":        map[string]any{"type": "string"},
			"addBlocks":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"addBlockedBy": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":     map[string]any{"type": "object"},
		},
		"required": []string{"taskId"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Task updates are auto-allowed — no user confirmation needed.
func (t *UpdateTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *UpdateTool) ValidateInput(input json.RawMessage) error {
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

func (t *UpdateTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		TaskID       string         `json:"taskId"`
		Status       *string        `json:"status,omitempty"`
		Subject      *string        `json:"subject,omitempty"`
		Description  *string        `json:"description,omitempty"`
		ActiveForm   *string        `json:"activeForm,omitempty"`
		Owner        *string        `json:"owner,omitempty"`
		AddBlocks    []string       `json:"addBlocks,omitempty"`
		AddBlockedBy []string       `json:"addBlockedBy,omitempty"`
		Metadata     map[string]any `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Handle "deleted" status as actual delete
	if p.Status != nil && *p.Status == "deleted" {
		if err := t.store.Delete(ctx, t.scopeID, p.TaskID); err != nil {
			return &types.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		return &types.ToolResult{
			Content:  fmt.Sprintf("task %s deleted", p.TaskID),
			Metadata: map[string]any{"render_hint": "task"},
		}, nil
	}

	updates := &task.TaskUpdate{
		Subject:      p.Subject,
		Description:  p.Description,
		ActiveForm:   p.ActiveForm,
		Owner:        p.Owner,
		AddBlocks:    p.AddBlocks,
		AddBlockedBy: p.AddBlockedBy,
		Metadata:     p.Metadata,
	}
	if p.Status != nil {
		s := task.TaskStatus(*p.Status)
		updates.Status = &s
	}

	updated, err := t.store.Update(ctx, t.scopeID, p.TaskID, updates)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	out, _ := json.Marshal(updated)
	return &types.ToolResult{
		Content:  string(out),
		Metadata: map[string]any{"render_hint": "task"},
	}, nil
}
