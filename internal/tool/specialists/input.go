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
			"description": "The pre-clarified task to delegate to the L2 Specialists coordinator. Specialists will decompose it into sub-agent steps, dispatch them, integrate results, and return a polished output. emma should call AskUserQuestion BEFORE this tool if the task is ambiguous — Specialists cannot ask the user.",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "Optional 3-5 word label for observability/logging.",
		},
	},
	"required": []string{"task"},
}
