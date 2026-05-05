package sendmessage

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// Tool implements the SendMessage tool for agent-to-agent communication.
type Tool struct {
	tool.BaseTool
	broker    *agent.MessageBroker
	agentName string // the name of the agent using this tool
	teamID    string
	logger    *zap.Logger
}

// New creates a SendMessage tool bound to a specific agent.
func New(broker *agent.MessageBroker, agentName, teamID string, logger *zap.Logger) *Tool {
	return &Tool{
		broker:    broker,
		agentName: agentName,
		teamID:    teamID,
		logger:    logger,
	}
}

func (t *Tool) Name() string            { return "SendMessage" }
func (t *Tool) Description() string      { return "向另一个 agent 发送消息。" }
func (t *Tool) IsReadOnly() bool         { return false }
func (t *Tool) IsConcurrencySafe() bool  { return true }

func (t *Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "接收方 agent 名字，或 '*' 表示广播。"},
			"message": map[string]any{"description": "消息内容（字符串或结构化对象）。"},
			"summary": map[string]any{"type": "string", "description": "5-10 词的简短预览。"},
		},
		"required": []string{"to", "message"},
	}
}

// CheckPermission implements tool.PermissionPreChecker.
// Agent messaging is auto-allowed — no user confirmation needed.
func (t *Tool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *Tool) ValidateInput(input json.RawMessage) error {
	var p struct {
		To      string          `json:"to"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return err
	}
	if p.To == "" {
		return fmt.Errorf("'to' is required")
	}
	if len(p.Message) == 0 {
		return fmt.Errorf("'message' is required")
	}
	return nil
}

func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p struct {
		To      string          `json:"to"`
		Message json.RawMessage `json:"message"`
		Summary string          `json:"summary"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	// Determine message content - could be string or structured
	var content string
	var msgType agent.MessageType = agent.MessageTypePlain

	// Try to unmarshal as string first
	var strContent string
	if err := json.Unmarshal(p.Message, &strContent); err == nil {
		content = strContent
	} else {
		// Check if it's a structured message (shutdown_request, etc.)
		var structured struct {
			Type      string `json:"type"`
			Reason    string `json:"reason"`
			RequestID string `json:"request_id"`
			Approved  *bool  `json:"approved"`
		}
		if err := json.Unmarshal(p.Message, &structured); err == nil && structured.Type != "" {
			switch structured.Type {
			case "shutdown_request":
				msgType = agent.MessageTypeShutdownRequest
			case "shutdown_response":
				msgType = agent.MessageTypeShutdownResponse
			}
			content = string(p.Message)
		} else {
			content = string(p.Message)
		}
	}

	msg := &agent.AgentMessage{
		From:    t.agentName,
		To:      p.To,
		Type:    msgType,
		Content: content,
		TeamID:  t.teamID,
	}

	if p.To == "*" {
		sent, err := t.broker.Broadcast(t.teamID, msg)
		if err != nil {
			return &types.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		t.logger.Debug("broadcast message", zap.String("from", t.agentName), zap.Int("recipients", sent))
		return &types.ToolResult{
			Content:  fmt.Sprintf("Message broadcast to %d recipients", sent),
			Metadata: map[string]any{"render_hint": "message"},
		}, nil
	}

	if err := t.broker.Send(msg); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}

	t.logger.Debug("sent message", zap.String("from", t.agentName), zap.String("to", p.To))
	return &types.ToolResult{
		Content:  fmt.Sprintf("Message sent to %s", p.To),
		Metadata: map[string]any{"render_hint": "message"},
	}, nil
}
