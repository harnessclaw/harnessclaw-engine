package tasktool

import (
	"context"
	"encoding/json"

	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type ListTool struct {
	tool.BaseTool
	store   task.Store
	scopeID string
}

func NewList(store task.Store, scopeID string) *ListTool {
	return &ListTool{store: store, scopeID: scopeID}
}

func (t *ListTool) Name() string             { return "TaskList" }
func (t *ListTool) Description() string       { return "List all tasks" }
func (t *ListTool) IsReadOnly() bool          { return true }
func (t *ListTool) IsConcurrencySafe() bool   { return true }

func (t *ListTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Task listing is auto-allowed — no user confirmation needed.
func (t *ListTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *ListTool) Execute(ctx context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	tasks, err := t.store.List(ctx, t.scopeID)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Build a status index from the already-loaded task list to avoid N+1 lookups.
	statusByID := make(map[string]task.TaskStatus, len(tasks))
	for _, tk := range tasks {
		statusByID[tk.ID] = tk.Status
	}

	// Return summary format
	type taskSummary struct {
		ID        string   `json:"id"`
		Subject   string   `json:"subject"`
		Status    string   `json:"status"`
		Owner     string   `json:"owner,omitempty"`
		BlockedBy []string `json:"blockedBy,omitempty"`
	}

	summaries := make([]taskSummary, 0, len(tasks))
	for _, tk := range tasks {
		// Filter blockedBy to only include open (non-completed) task IDs
		var openBlockers []string
		for _, bid := range tk.BlockedBy {
			if st, ok := statusByID[bid]; ok && st != task.TaskStatusCompleted {
				openBlockers = append(openBlockers, bid)
			}
		}
		summaries = append(summaries, taskSummary{
			ID:        tk.ID,
			Subject:   tk.Subject,
			Status:    string(tk.Status),
			Owner:     tk.Owner,
			BlockedBy: openBlockers,
		})
	}

	out, _ := json.Marshal(summaries)
	return &types.ToolResult{
		Content:  string(out),
		Metadata: map[string]any{"render_hint": "task"},
	}, nil
}
