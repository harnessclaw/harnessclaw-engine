package videogen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
	"go.uber.org/zap"
)

const ToolNameVideoCreate = "video_create"

var allowedRatios = map[string]bool{
	"16:9": true, "4:3": true, "1:1": true, "3:4": true,
	"9:16": true, "21:9": true, "adaptive": true,
}

type VideoCreateTool struct {
	tool.BaseTool
	source   ConfigSource
	registry *ProviderRegistry
	rootDir  string
	logger   *zap.Logger
}

func NewCreate(source ConfigSource, registry *ProviderRegistry, rootDir string, logger *zap.Logger) *VideoCreateTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &VideoCreateTool{
		source:   source,
		registry: registry,
		rootDir:  rootDir,
		logger:   logger.Named("video_create"),
	}
}

func (*VideoCreateTool) Name() string                 { return ToolNameVideoCreate }
func (*VideoCreateTool) Description() string           { return videoCreateDescription }
func (*VideoCreateTool) IsReadOnly() bool              { return false }
func (*VideoCreateTool) IsConcurrencySafe() bool       { return true }
func (*VideoCreateTool) SafetyLevel() tool.SafetyLevel { return tool.SafetyCaution }

func (t *VideoCreateTool) IsEnabled() bool {
	if t.source == nil || t.registry == nil {
		return false
	}
	ref := t.source.AgentVideoGeneration()
	if ref == "" {
		return false
	}
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return false
	}
	_, ok = t.registry.Get(ep.Provider)
	return ok
}

func (*VideoCreateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Prompt for the video (Chinese or English). Required for text-to-video; optional but recommended for image-to-video. Keep <= 400 chars.",
			},
			"image_url": map[string]any{
				"type":        "string",
				"description": "Image-to-video: external URL of the first frame (JPEG/PNG). Mutually exclusive with image_b64; image_url wins if both given.",
			},
			"image_b64": map[string]any{
				"type":        "string",
				"description": "Image-to-video: first frame as a base64 data URI (data:image/png;base64,...).",
			},
			"duration_s": map[string]any{
				"type":        "integer",
				"enum":        []int{5, 10},
				"description": "Video length in seconds. Default 5.",
			},
			"aspect_ratio": map[string]any{
				"type":        "string",
				"enum":        []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"},
				"description": "Aspect ratio. adaptive follows the input image (image-to-video). Default 16:9.",
			},
			"seed": map[string]any{
				"type":        "integer",
				"description": "Random seed. Omit for random; a fixed seed with identical params reproduces a similar result.",
			},
		},
		"required": []string{"prompt"},
	}
}

type createInput struct {
	Prompt      string `json:"prompt"`
	ImageURL    string `json:"image_url"`
	ImageB64    string `json:"image_b64"`
	DurationS   int    `json:"duration_s"`
	AspectRatio string `json:"aspect_ratio"`
	Seed        *int   `json:"seed"`
}

func parseCreateInput(raw json.RawMessage) (createInput, error) {
	var in createInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return createInput{}, err
	}
	return in, nil
}

func (t *VideoCreateTool) ValidateInput(raw json.RawMessage) error {
	in, err := parseCreateInput(raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if in.DurationS != 0 && in.DurationS != 5 && in.DurationS != 10 {
		return fmt.Errorf("duration_s must be 5 or 10, got %d", in.DurationS)
	}
	if in.AspectRatio != "" && !allowedRatios[in.AspectRatio] {
		return fmt.Errorf("aspect_ratio %q is not supported", in.AspectRatio)
	}
	return nil
}

func (t *VideoCreateTool) Execute(ctx context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	in, err := parseCreateInput(raw)
	if err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if err := t.ValidateInput(raw); err != nil {
		return errResult("invalid input: "+err.Error(), types.ToolErrorInvalidInput), nil
	}
	if t.source == nil || t.registry == nil {
		return errResult("video_create is not configured", types.ToolErrorInternal), nil
	}

	ref := t.source.AgentVideoGeneration()
	ep, ok := t.source.ResolveEndpoint(ref)
	if !ok {
		return errResult("video_create: no usable video endpoint configured (check Settings -> video generation)", types.ToolErrorInternal), nil
	}
	provider, ok := t.registry.Get(ep.Provider)
	if !ok {
		return errResult(fmt.Sprintf("video_create: provider %q not implemented", ep.Provider), types.ToolErrorInternal), nil
	}

	dur := in.DurationS
	if dur == 0 {
		dur = 5
	}
	ratio := in.AspectRatio
	if ratio == "" {
		ratio = "16:9"
	}

	out, err := provider.SubmitTask(ctx, SubmitRequest{
		Endpoint:    ep,
		Prompt:      in.Prompt,
		ImageURL:    in.ImageURL,
		ImageB64:    in.ImageB64,
		DurationS:   dur,
		AspectRatio: ratio,
		Seed:        in.Seed,
	})
	if err != nil {
		return classifyProviderError("video submit failed", err), nil
	}

	endpointRef := ep.Provider + ":" + ep.Endpoint
	return &types.ToolResult{
		Content: fmt.Sprintf("video task submitted (%s); call video_query with task_id %q to fetch the result.", endpointRef, out.TaskID),
		Metadata: map[string]any{
			"task_id":      out.TaskID,
			"submitted_at": out.SubmittedAt.UTC().Format(time.RFC3339),
			"endpoint":     endpointRef,
		},
	}, nil
}

// classifyProviderError maps a provider error's sentinel class to a ToolResult
// error type. Used by both create and query.
func classifyProviderError(prefix string, err error) *types.ToolResult {
	switch {
	case errors.Is(err, ErrPermissionDenied):
		return errResult(prefix+": "+err.Error(), types.ToolErrorPermissionDenied)
	case errors.Is(err, ErrValidation):
		return errResult(prefix+": "+err.Error(), types.ToolErrorInvalidInput)
	default:
		return errResult(prefix+": "+err.Error(), types.ToolErrorDependencyFail)
	}
}
