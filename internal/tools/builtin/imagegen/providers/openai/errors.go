package openai

import (
	"net/http"

	imagegen "harnessclaw-go/internal/tools/builtin/imagegen"
)

// classifyHTTP maps an HTTP status + error body to a sentinel-wrapped error.
// 401/403 → permission; other 4xx → validation; 5xx/429 → transient.
func classifyHTTP(code int, gerr *genError, raw []byte) error {
	msg := http.StatusText(code)
	if gerr != nil && gerr.Message != "" {
		msg = gerr.Message
		if gerr.Code != "" {
			msg += " (" + gerr.Code + ")"
		}
	} else if len(raw) > 0 {
		msg = string(raw)
	}
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return imagegen.ErrPermissionDeniedf("openai-image %d: %s", code, msg)
	case code >= 500 || code == http.StatusTooManyRequests:
		return imagegen.ErrTransientf("openai-image %d: %s", code, msg)
	default:
		return imagegen.ErrValidationf("openai-image %d: %s", code, msg)
	}
}
