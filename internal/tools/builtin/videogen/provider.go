package videogen

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// VideoProvider is the provider-agnostic seam the video tools call.
// Implementations (doubao, future runway/keling) live under providers/.
type VideoProvider interface {
	Name() string
	SubmitTask(ctx context.Context, req SubmitRequest) (*SubmitResult, error)
	QueryTask(ctx context.Context, req QueryRequest) (*QueryResult, error)
	DownloadVideo(ctx context.Context, url string) (data []byte, mime string, err error)
}

// EndpointRef is a fully resolved endpoint: identity (Provider/Endpoint/Model)
// plus the credentials the provider needs to make a call. The tool layer fills
// every field from ConfigSource before handing it to a provider, so providers
// hold no per-config state and can be registered as singletons.
type EndpointRef struct {
	Provider string
	Endpoint string
	Model    string
	APIKey   string
	BaseURL  string // "" → provider falls back to its built-in default
}

type SubmitRequest struct {
	Endpoint    EndpointRef
	Prompt      string
	ImageURL    string // image-to-video; takes priority over ImageB64
	ImageB64    string // data URI form
	DurationS   int    // 5 or 10
	AspectRatio string // "16:9" etc.
	Seed        *int   // nil = omit (random)
}

type SubmitResult struct {
	TaskID      string
	SubmittedAt time.Time
}

type QueryRequest struct {
	Endpoint EndpointRef
	TaskID   string
}

type TaskStatus string

const (
	StatusQueued    TaskStatus = "queued"
	StatusRunning   TaskStatus = "running"
	StatusSucceeded TaskStatus = "succeeded"
	StatusFailed    TaskStatus = "failed"
	StatusExpired   TaskStatus = "expired"
	StatusCancelled TaskStatus = "cancelled"
	StatusNotFound  TaskStatus = "not_found"
)

type QueryResult struct {
	Status       TaskStatus
	VideoURL     string    // Succeeded: non-empty
	URLExpiresAt time.Time // Succeeded: usually updated_at + 24h
	Model        string
	Resolution   string
	Ratio        string
	Duration     int
	ErrorCode    string // Failed
	ErrorMessage string // Failed
}

// Provider error contract:
//   - nil error + valid QueryResult/SubmitResult → tool branches on status.
//   - non-nil error wrapping ErrTransient → tool retries (exp backoff, max 3).
//   - non-nil error wrapping ErrPermissionDenied / ErrValidation → tool returns
//     immediately, no retry.
//   - any other non-nil error → treated as transient.
var (
	ErrPermissionDenied = errors.New("videogen: permission denied")
	ErrValidation       = errors.New("videogen: validation error")
	ErrTransient        = errors.New("videogen: transient error")
)

// wrapErr attaches a sentinel class to a human-readable message.
func wrapErr(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), sentinel)
}

// Exported formatting wrappers so provider subpackages can construct
// sentinel-classed errors.
func ErrPermissionDeniedf(format string, a ...any) error { return wrapErr(ErrPermissionDenied, format, a...) }
func ErrValidationf(format string, a ...any) error       { return wrapErr(ErrValidation, format, a...) }
func ErrTransientf(format string, a ...any) error        { return wrapErr(ErrTransient, format, a...) }
