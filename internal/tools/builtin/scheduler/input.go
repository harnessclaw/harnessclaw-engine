package scheduler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// schedulerInput is the parsed payload for the dispatch tool.
//
// emma sends a pre-clarified `task` plus `subagent_type` to pick which
// agent runs it. The chosen agent (resolved via AgentDefinitionRegistry)
// executes the task and returns the integrated result.
type schedulerInput struct {
	// Task is the natural-language description of what to do. Required.
	// emma should have already clarified ambiguity via ask_user_question
	// before calling this tool.
	Task string `json:"task"`

	// SubagentType picks the agent that runs the task. Required.
	// Must match a registered AgentDefinition.Name in the registry —
	// emma sees the available roster via the team section in its
	// system prompt.
	SubagentType string `json:"subagent_type"`

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
	if strings.TrimSpace(i.SubagentType) == "" {
		return fmt.Errorf("subagent_type is required")
	}
	return nil
}

var inputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"task": map[string]any{
			"type":        "string",
			"description": "已澄清完毕的任务，派给指定的 sub-agent 执行。任务有歧义时 emma 必须先 ask_user_question 澄清——sub-agent 不能向用户追问。",
		},
		"subagent_type": map[string]any{
			"type":        "string",
			"description": "派遣给哪个 sub-agent。值必须是当前可用 agent 名册中的一个 codename（见 system prompt 的「我的搭档」表）。",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "可选的 3-5 词标签，便于观测 / 日志。",
		},
	},
	"required": []string{"task", "subagent_type"},
}
