package failover

import (
	"context"
	"errors"
	"testing"

	"harnessclaw-go/internal/provider/retry"
)

func TestFailoverWorthy_FallbackTriggeredAlwaysCrosses(t *testing.T) {
	err := &retry.FallbackTriggeredError{Consecutive529: 3, Message: "fallback"}
	if !FailoverWorthy(err) {
		t.Fatalf("FallbackTriggeredError must be failover-worthy")
	}
}

func TestFailoverWorthy_TransientHTTPClasses(t *testing.T) {
	for _, tc := range []retry.APIErrorType{
		retry.ErrOverloaded,
		retry.ErrRateLimit,
		retry.ErrServerError,
		retry.ErrNetworkError,
		retry.ErrAuthFailed,
		retry.ErrTokenRevoked,
		retry.ErrUnknown,
	} {
		err := &retry.APIError{Type: tc, Message: string(tc)}
		if !FailoverWorthy(err) {
			t.Errorf("type %s should be failover-worthy", tc)
		}
	}
}

func TestFailoverWorthy_InputErrorsDoNotCross(t *testing.T) {
	for _, tc := range []retry.APIErrorType{
		retry.ErrPromptTooLong,
		retry.ErrMaxOutputTokens,
	} {
		err := &retry.APIError{Type: tc, Message: string(tc)}
		if FailoverWorthy(err) {
			t.Errorf("type %s must NOT be failover-worthy (input issue, same on every provider)", tc)
		}
	}
}

func TestFailoverWorthy_CallerCanceled(t *testing.T) {
	if FailoverWorthy(context.Canceled) {
		t.Fatalf("context.Canceled must not cross providers (caller-initiated)")
	}
	if FailoverWorthy(context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded must not cross providers (caller-initiated)")
	}
}

func TestFailoverWorthy_Nil(t *testing.T) {
	if FailoverWorthy(nil) {
		t.Fatalf("nil error must not be failover-worthy")
	}
}

func TestFailoverWorthy_PlainStringWrappedAPIErrorDoesNotQualify(t *testing.T) {
	apiErr := &retry.APIError{Type: retry.ErrServerError, Message: "500"}
	wrapped := errors.New("decorator: " + apiErr.Error())
	if FailoverWorthy(wrapped) {
		t.Fatalf("plain string-wrapped error should not cross")
	}
}

func TestFailoverWorthy_ErrorsAsWrappedAPIErrorQualifies(t *testing.T) {
	apiErr := &retry.APIError{Type: retry.ErrServerError, Message: "500"}
	wrappedAs := &wrappingErr{inner: apiErr}
	if !FailoverWorthy(wrappedAs) {
		t.Fatalf("errors.As-wrapped APIError must be failover-worthy")
	}
}

type wrappingErr struct{ inner *retry.APIError }

func (w *wrappingErr) Error() string { return "wrap: " + w.inner.Error() }
func (w *wrappingErr) Unwrap() error { return w.inner }
