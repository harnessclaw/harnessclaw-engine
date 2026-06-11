package doubao

import (
	"net/http"

	videogen "harnessclaw-go/internal/tools/builtin/videogen"
)

// arkHTTPError maps an HTTP status + Ark error body to a sentinel-wrapped error.
// 401/403 → permission denied; other 4xx → validation; 5xx/429 → transient.
func arkHTTPError(code int, arkErr *arkError, raw []byte) error {
	msg := "ark http " + http.StatusText(code)
	if arkErr != nil && arkErr.Message != "" {
		msg = arkErr.Message + " (" + arkErr.Code + ")"
	} else if len(raw) > 0 {
		msg = string(raw)
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return videogen.ErrPermissionDeniedf("doubao %d: %s", code, msg)
	case code >= 500 || code == http.StatusTooManyRequests:
		return videogen.ErrTransientf("doubao %d: %s", code, msg)
	default: // other 4xx
		return videogen.ErrValidationf("doubao %d: %s", code, msg)
	}
}

// mapStatus converts an Ark task status string to the generic TaskStatus.
// Unknown values are treated as failed (safest — stops polling, surfaces to LLM).
func mapStatus(s string) videogen.TaskStatus {
	switch s {
	case "queued":
		return videogen.StatusQueued
	case "running":
		return videogen.StatusRunning
	case "succeeded":
		return videogen.StatusSucceeded
	case "failed":
		return videogen.StatusFailed
	case "expired":
		return videogen.StatusExpired
	case "cancelled":
		return videogen.StatusCancelled
	default:
		return videogen.StatusFailed
	}
}
