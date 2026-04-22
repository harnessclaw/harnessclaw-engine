package agenttool

import (
	"encoding/json"
	"fmt"
)

// agentInput is the parsed input for the Agent tool.
type agentInput struct {
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	Description     string `json:"description"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	Fork            bool   `json:"fork"`
	RunInBackground bool   `json:"run_in_background"`
}

// parseInput unmarshals raw JSON into agentInput.
func parseInput(raw json.RawMessage) (*agentInput, error) {
	var input agentInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("invalid Agent tool input: %w", err)
	}
	return &input, nil
}

// validate checks that the input has all required fields and valid values.
func (i *agentInput) validate() error {
	if i.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}

	// Validate subagent_type if provided.
	if i.SubagentType != "" {
		switch i.SubagentType {
		case "general-purpose", "Explore", "explore", "Plan", "plan":
			// valid
		default:
			return fmt.Errorf("unsupported subagent_type: %s (valid: general-purpose, Explore, Plan)", i.SubagentType)
		}
	}

	return nil
}

// inputSchema is the JSON Schema for the Agent tool's input.
var inputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"prompt": map[string]any{
			"type":        "string",
			"description": "The task instruction for the sub-agent to execute.",
		},
		"subagent_type": map[string]any{
			"type":        "string",
			"description": "The type of sub-agent to spawn. Controls tool access and prompt profile.",
			"enum":        []string{"general-purpose", "Explore", "Plan"},
		},
		"description": map[string]any{
			"type":        "string",
			"description": "A short (3-5 word) description of what the sub-agent will do.",
		},
		"name": map[string]any{
			"type":        "string",
			"description": "Optional name for the sub-agent, used for identification in logs.",
		},
		"model": map[string]any{
			"type":        "string",
			"description": "Optional model override for this sub-agent. Empty inherits from parent.",
		},
		"fork": map[string]any{
			"type":        "boolean",
			"description": "When true, the sub-agent inherits the parent's conversation context.",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "When true, the agent runs asynchronously and returns an agent ID immediately.",
		},
	},
	"required": []string{"prompt"},
}
