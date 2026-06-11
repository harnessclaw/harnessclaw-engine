package doubao

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	videogen "harnessclaw-go/internal/tools/builtin/videogen"
)

func TestArkHTTPErrorClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code     int
		sentinel error
	}{
		{http.StatusUnauthorized, videogen.ErrPermissionDenied},
		{http.StatusForbidden, videogen.ErrPermissionDenied},
		{http.StatusBadRequest, videogen.ErrValidation},
		{http.StatusNotFound, videogen.ErrValidation},
		{http.StatusInternalServerError, videogen.ErrTransient},
		{http.StatusTooManyRequests, videogen.ErrTransient},
	}
	for _, c := range cases {
		err := arkHTTPError(c.code, &arkError{Code: "X", Message: "msg"}, []byte(`{}`))
		if !errors.Is(err, c.sentinel) {
			t.Fatalf("code %d: expected sentinel %v, got %v", c.code, c.sentinel, err)
		}
		if !strings.Contains(err.Error(), "msg") {
			t.Fatalf("code %d: message not propagated: %q", c.code, err.Error())
		}
	}
}

func TestMapStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]videogen.TaskStatus{
		"queued":    videogen.StatusQueued,
		"running":   videogen.StatusRunning,
		"succeeded": videogen.StatusSucceeded,
		"failed":    videogen.StatusFailed,
		"expired":   videogen.StatusExpired,
		"cancelled": videogen.StatusCancelled,
		"weird":     videogen.StatusFailed,
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Fatalf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
