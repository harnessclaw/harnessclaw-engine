package artifacttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ReadToolName is the LLM-facing name. See WriteToolName for the
// PascalCase rationale.
const ReadToolName = "ArtifactRead"

// readInput is the parsed payload.
type readInput struct {
	ArtifactID string `json:"artifact_id"`
	// Mode selects the level of detail. Default 'preview' — that's the
	// "scan first, fetch full only if needed" behaviour doc §5 trains the
	// LLM into.
	Mode string `json:"mode,omitempty"`
}

// ReadTool fetches an artifact by ID at the requested detail level.
type ReadTool struct {
	tool.BaseTool
}

// NewReadTool returns the registered tool instance.
func NewReadTool() *ReadTool {
	return &ReadTool{}
}

func (*ReadTool) Name() string             { return ReadToolName }
func (*ReadTool) Description() string      { return readDescription }
func (*ReadTool) IsReadOnly() bool         { return true }
func (*ReadTool) IsEnabled() bool          { return true }
func (*ReadTool) IsConcurrencySafe() bool  { return true }

// InputSchema describes the LLM-facing arguments.
func (*ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"artifact_id": map[string]any{
				"type":        "string",
				"description": "The ID returned by ArtifactWrite (format art_<hex>).",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"metadata", "preview", "full"},
				"description": "How much to return. 'metadata' = name/description/preview only. 'preview' = same as metadata (kept separate to mirror the doc; the actual preview is included in either). 'full' = entire content. Default: preview.",
			},
		},
		"required": []string{"artifact_id"},
	}
}

// ValidateInput rejects calls the store would reject anyway. ID format
// check filters out obvious hallucinations (doc §12).
func (*ReadTool) ValidateInput(raw json.RawMessage) error {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if !artifact.IsValidID(in.ArtifactID) {
		return fmt.Errorf("artifact_id has invalid format: %q (expected art_<24 hex chars>)", in.ArtifactID)
	}
	if in.Mode == "" {
		return nil
	}
	if !artifact.IsValidMode(artifact.ReadMode(in.Mode)) {
		return fmt.Errorf("mode must be metadata|preview|full, got %q", in.Mode)
	}
	return nil
}

// Execute looks up the artifact and returns the requested detail level.
func (*ReadTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return errResult("invalid input: " + err.Error()), nil
	}

	store, ok := getStore(ctx)
	if !ok {
		return errResult("artifact store not configured for this engine"), nil
	}

	mode := artifact.ReadMode(in.Mode)
	if mode == "" {
		mode = artifact.ModePreview
	}

	a, err := store.Get(ctx, in.ArtifactID)
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			return errResult(fmt.Sprintf("artifact %s not found (may have expired or never existed)", in.ArtifactID)), nil
		}
		if errors.Is(err, artifact.ErrAccessDenied) {
			return errResult(fmt.Sprintf("artifact %s is not readable in this scope", in.ArtifactID)), nil
		}
		return errResult("read artifact: " + err.Error()), nil
	}

	// Strip Content for the lighter modes so the LLM doesn't accidentally
	// see the full payload when it asked for "metadata".
	switch mode {
	case artifact.ModeMetadata, artifact.ModePreview:
		a.Content = ""
	}

	body, _ := json.Marshal(struct {
		Mode     artifact.ReadMode `json:"mode"`
		Artifact *artifact.Artifact `json:"artifact"`
	}{
		Mode:     mode,
		Artifact: a,
	})

	// render_hint = "artifact_view" — this is a READ, not a production.
	// Doc §10: only writes (render_hint=artifact) populate Engine event
	// Artifacts so the UI doesn't double-count a parent's artifact as if
	// the reader produced it. Two hints, two intents:
	//   - "artifact"      → producer wrote a new artifact (subagent_end aggregation)
	//   - "artifact_view" → consumer fetched an existing one (no aggregation)
	return &types.ToolResult{
		Content: string(body),
		Metadata: map[string]any{
			"render_hint": "artifact_view",
			"artifact_id": a.ID,
			"mode":        string(mode),
			"name":        a.Name,
			"type":        string(a.Type),
		},
	}, nil
}

const readDescription = `Fetch a stored artifact by ID.

Modes (use the lightest one that answers your question):
- metadata: name + description + preview + size. Use this to decide IF you need the artifact.
- preview:  same as metadata (the preview field is already populated). Default.
- full:     entire content. Only request this when you need the bytes — e.g. you must summarise/transform the content yourself.

Calling this on an unknown or expired artifact returns an error; treat it as recoverable (re-plan or ask).`
