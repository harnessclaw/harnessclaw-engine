// Package artifacttool provides tools for interacting with the artifact store.
package artifacttool

import (
	"context"
	"encoding/json"
	"fmt"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type getInput struct {
	ArtifactID string `json:"artifact_id"`
}

// GetTool retrieves the full content of a stored artifact by ID.
// The LLM uses this to access large tool results that were previously
// truncated and persisted to save input tokens.
type GetTool struct {
	tool.BaseTool
}

// NewGetTool returns a new ArtifactGet tool instance.
func NewGetTool() *GetTool {
	return &GetTool{}
}

func (t *GetTool) Name() string        { return "ArtifactGet" }
func (t *GetTool) Description() string  { return artifactGetDescription }
func (t *GetTool) IsReadOnly() bool     { return true }
func (t *GetTool) IsEnabled() bool      { return true }

func (t *GetTool) IsConcurrencySafe() bool { return true }

func (t *GetTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"artifact_id": map[string]any{
				"type":        "string",
				"description": "The artifact ID to retrieve (e.g., art_abc12345)",
			},
		},
		"required": []string{"artifact_id"},
	}
}

func (t *GetTool) ValidateInput(input json.RawMessage) error {
	var in getInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if in.ArtifactID == "" {
		return fmt.Errorf("artifact_id is required")
	}
	return nil
}

func (t *GetTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var in getInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &types.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	store, ok := tool.GetArtifactStore(ctx)
	if !ok {
		return &types.ToolResult{Content: "artifact store not available", IsError: true}, nil
	}

	art := store.Get(in.ArtifactID)
	if art.ID == "" {
		return &types.ToolResult{
			Content: fmt.Sprintf("artifact %q not found", in.ArtifactID),
			IsError: true,
		}, nil
	}

	return &types.ToolResult{
		Content: art.Content,
		Metadata: map[string]any{
			"artifact_id": art.ID,
			"size":        art.Size,
		},
	}, nil
}

const artifactGetDescription = `Retrieve the full content of a stored artifact by its ID.

When tool results are large, they are automatically persisted as artifacts and
replaced with a truncated preview in the conversation. Use this tool to retrieve
the full, untruncated content when you need it.

Common use cases:
- Reading the full content of a file that was previously read and truncated
- Getting the complete output of a command whose result was summarized
- Retrieving content to write to a new file (use with the Write tool)`
