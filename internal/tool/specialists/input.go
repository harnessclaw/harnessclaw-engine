package specialists

import (
	"encoding/json"
	"fmt"
	"strings"
)

// specialistsInput is the parsed payload for the Specialists tool.
//
// emma sends a single, pre-clarified `task`. Specialists takes it from
// there: decompose, dispatch L3 sub-agents, integrate, return.
type specialistsInput struct {
	// Task is the natural-language description of what to do. Required.
	// emma should have already clarified ambiguity via AskUserQuestion
	// before calling this tool.
	Task string `json:"task"`

	// Description is an optional 3-5 word observability label.
	Description string `json:"description"`
}

func parseInput(raw json.RawMessage) (*specialistsInput, error) {
	var in specialistsInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid Specialists input: %w", err)
	}
	return &in, nil
}

func (i *specialistsInput) validate() error {
	if strings.TrimSpace(i.Task) == "" {
		return fmt.Errorf("task is required")
	}
	return nil
}

var inputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"task": map[string]any{
			"type":        "string",
			"description": "已澄清完毕的任务，派给 L2 Specialists 协调者。Specialists 会拆步、派 sub-agent、整合结果、返回打磨好的产出。任务有歧义时 emma 必须先 AskUserQuestion 澄清——Specialists 不能向用户追问。",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "可选的 3-5 词标签，便于观测 / 日志。",
		},
	},
	"required": []string{"task"},
}
