package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// orchestrateInput is the parsed payload for the Orchestrate tool.
type orchestrateInput struct {
	// Intent is the natural-language description of the multi-step task.
	// emma writes one line; the Planner expands it into a plan.
	Intent string `json:"intent"`

	// Description is an optional short observability label.
	Description string `json:"description"`

	// AvailableAgents is an optional caller-provided roster override.
	// When empty, the tool uses the runtime roster from the engine.
	AvailableAgents []string `json:"available_agents"`
}

func parseInput(raw json.RawMessage) (*orchestrateInput, error) {
	var in orchestrateInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid Orchestrate input: %w", err)
	}
	return &in, nil
}

func (i *orchestrateInput) validate() error {
	if strings.TrimSpace(i.Intent) == "" {
		return fmt.Errorf("intent is required")
	}
	return nil
}

var inputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"intent": map[string]any{
			"type":        "string",
			"description": "多步任务的自然语言描述。Planner 会把它展开成 sub-agent 步骤的依赖 DAG。",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "可选的 3-5 词标签，便于观测 / 日志。",
		},
		"available_agents": map[string]any{
			"type":        "array",
			"description": "可选的 agent 名单覆盖。为空时使用引擎已注册的全部 agent。",
			"items":       map[string]any{"type": "string"},
		},
	},
	"required": []string{"intent"},
}
