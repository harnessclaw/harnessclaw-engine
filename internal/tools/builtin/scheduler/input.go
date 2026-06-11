package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// schedulerInput is the parsed payload for the scheduler tool.
//
// emma sends a single, pre-clarified `task`. scheduler takes it from
// there: decompose, dispatch the L3 sub-agent, integrate, return.
type schedulerInput struct {
	// Task is the natural-language description of what to do. Required.
	// emma should have already clarified ambiguity via ask_user_question
	// before calling this tool.
	Task string `json:"task"`

	// Description is an optional 3-5 word observability label.
	Description string `json:"description"`
}

func parseInput(raw json.RawMessage) (*schedulerInput, error) {
	var in schedulerInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid scheduler input: %w", err)
	}
	return &in, nil
}

func (i *schedulerInput) validate() error {
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
			"description": "已澄清完毕的任务，派给 L2 scheduler 协调者。scheduler 会拆步、派 sub-agent、整合结果、返回打磨好的产出。任务有歧义时 emma 必须先 ask_user_question 澄清——scheduler 不能向用户追问。",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "可选的 3-5 词标签，便于观测 / 日志。",
		},
	},
	"required": []string{"task"},
}
