package failover

import (
	"context"
	"errors"

	"harnessclaw-go/internal/provider/retry"
)

// FailoverWorthy reports whether `err` should cause the failover
// dispatcher to trip the current provider and route to the next one
// in the chain. Rules:
//
//   - retry.FallbackTriggeredError → yes (consecutive 529s; primary
//     is clearly stressed and the retry engine has already given up)
//   - retry.APIError with a transient or auth class → yes
//   - retry.APIError with prompt_too_long / max_output_tokens → no
//     (the same payload will fail on every provider)
//   - context.Canceled / context.DeadlineExceeded → no
//     (caller-initiated cancellation; bubble up)
//   - everything else → no (caller is responsible for raising it)
//
// nil returns false so callers can short-circuit a success path.
func FailoverWorthy(err error) bool {
	if err == nil {
		return false
	}

	// Caller-initiated cancellation never crosses providers.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var fbErr *retry.FallbackTriggeredError
	if errors.As(err, &fbErr) {
		return true
	}

	var apiErr *retry.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Type {
		case retry.ErrPromptTooLong, retry.ErrMaxOutputTokens:
			return false
		case retry.ErrOverloaded,
			retry.ErrRateLimit,
			retry.ErrServerError,
			retry.ErrNetworkError,
			retry.ErrAuthFailed,
			retry.ErrTokenRevoked,
			retry.ErrUnknown:
			return true
		default:
			return false
		}
	}

	return false
}
