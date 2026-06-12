package imagegen

import (
	"context"
	"errors"
	"fmt"
)

// ImageProvider is the provider-agnostic seam the image tool calls. Image
// generation is synchronous (one request returns the images), so unlike
// VideoProvider there is no submit/poll — just Generate.
type ImageProvider interface {
	Name() string
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error)
}

// ImageEndpointRef is a fully resolved image endpoint: identity + credentials
// + the API path. The tool layer fills every field from ImageGenSource before
// handing it to a provider, so providers hold no per-config state.
type ImageEndpointRef struct {
	Provider   string
	Endpoint   string
	Model      string
	APIKey     string
	BaseURL    string
	Path       string // "" → provider default (e.g. /v1/images/generations)
	AuthHeader string // "" → "Authorization"
	AuthPrefix string // "" → "Bearer "
}

type GenerateRequest struct {
	Endpoint ImageEndpointRef
	Prompt   string
	N        int    // number of images
	Size     string // "1024x1024" etc.
	Quality  string // optional
	Style    string // optional
}

type GeneratedImageData struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
	MIME          string
}

type GenerateResult struct {
	Images []GeneratedImageData
}

// Provider error contract:
//   - nil error + GenerateResult → tool uses images.
//   - error wrapping ErrTransient → tool retries (exp backoff, max 3).
//   - error wrapping ErrPermissionDenied / ErrValidation → return immediately.
//   - any other error → treated as transient.
var (
	ErrPermissionDenied = errors.New("imagegen: permission denied")
	ErrValidation       = errors.New("imagegen: validation error")
	ErrTransient        = errors.New("imagegen: transient error")
)

func wrapImgErr(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), sentinel)
}

// Exported sentinel-classed constructors so provider subpackages can build them.
func ErrPermissionDeniedf(format string, a ...any) error { return wrapImgErr(ErrPermissionDenied, format, a...) }
func ErrValidationf(format string, a ...any) error       { return wrapImgErr(ErrValidation, format, a...) }
func ErrTransientf(format string, a ...any) error        { return wrapImgErr(ErrTransient, format, a...) }
