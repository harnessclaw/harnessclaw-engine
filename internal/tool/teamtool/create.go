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

// CreateTool implements the TeamCreate tool for creating agent teams.
type CreateTool struct {
	tool.BaseTool
	teamMgr *agent.TeamManager
	broker  *agent.MessageBroker
	logger  *zap.Logger
}

// NewCreate creates a TeamCreate tool.
func NewCreate(teamMgr *agent.TeamManager, broker *agent.MessageBroker, logger *zap.Logger) *CreateTool {
	return &CreateTool{
		teamMgr: teamMgr,
		broker:  broker,
		logger:  logger,
	}
}

func (t *CreateTool) Name() string            { return "TeamCreate" }
func (t *CreateTool) Description() string      { return "新建一个 team，用来协调多个 agent。" }
func (t *CreateTool) IsReadOnly() bool         { return false }
func (t *CreateTool) IsConcurrencySafe() bool  { return true }

func (t *CreateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"team_name":   map[string]any{"type": "string", "description": "新 team 的名字。"},
			"description": map[string]any{"type": "string", "description": "team 的简介或用途。"},
		},
		"required": []string{"team_name"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Team creation is auto-allowed — no user confirmation needed.
func (t *CreateTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *CreateTool) ValidateInput(input json.RawMessage) error {
	var p struct {
		TeamName string `json:"team_name"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return err
	}
	if p.TeamName == "" {
		return fmt.Errorf("team_name is required")
	}
	return nil
}

func (t *CreateTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		TeamName    string `json:"team_name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Use the caller's context agent name as leader (empty for now; set by integration layer).
	leaderName := ""
	team, err := t.teamMgr.Create(p.TeamName, p.Description, leaderName)
	if err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	t.logger.Info("team created",
		zap.String("team", team.Name),
		zap.String("id", team.ID),
	)

	out, _ := json.Marshal(team)
	return &types.ToolResult{
		Content:  string(out),
		Metadata: map[string]any{"render_hint": "team"},
	}, nil
}
