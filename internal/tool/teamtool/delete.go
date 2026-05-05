package teamtool

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// DeleteTool implements the TeamDelete tool for removing teams.
type DeleteTool struct {
	tool.BaseTool
	teamMgr *agent.TeamManager
	logger  *zap.Logger
}

// NewDelete creates a TeamDelete tool.
func NewDelete(teamMgr *agent.TeamManager, logger *zap.Logger) *DeleteTool {
	return &DeleteTool{
		teamMgr: teamMgr,
		logger:  logger,
	}
}

func (t *DeleteTool) Name() string            { return "TeamDelete" }
func (t *DeleteTool) Description() string      { return "删除一个 team 及其任务资源。" }
func (t *DeleteTool) IsReadOnly() bool         { return false }
func (t *DeleteTool) IsConcurrencySafe() bool  { return true }

func (t *DeleteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"team_name": map[string]any{"type": "string", "description": "要删除的 team 名字。"},
		},
		"required": []string{"team_name"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Team deletion is auto-allowed — no user confirmation needed.
func (t *DeleteTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *DeleteTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		TeamName string `json:"team_name"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if p.TeamName == "" {
		return &types.ToolResult{Content: "team_name is required", IsError: true}, nil
	}

	if err := t.teamMgr.Delete(p.TeamName); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	t.logger.Info("team deleted", zap.String("team", p.TeamName))
	return &types.ToolResult{
		Content:  fmt.Sprintf("team %q deleted", p.TeamName),
		Metadata: map[string]any{"render_hint": "team"},
	}, nil
}
