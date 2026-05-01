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
			"description": "Natural-language description of the multi-step task. The Planner expands this into a dependency graph of sub-agent steps.",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "Optional short (3-5 word) label for observability/logging.",
		},
		"available_agents": map[string]any{
			"type":        "array",
			"description": "Optional roster override. When empty, the engine's registered agents are used.",
			"items":       map[string]any{"type": "string"},
		},
	},
	"required": []string{"intent"},
}
