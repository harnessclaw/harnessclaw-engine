package videogen

import (
	"errors"
	"testing"
)

func TestSentinelErrorsAreDistinct(t *testing.T) {
	t.Parallel()
	wrapped := errors.New("doubao: bad key: permission denied")
	if errors.Is(wrapped, ErrPermissionDenied) {
		t.Fatal("plain error should not match ErrPermissionDenied without %w wrap")
	}
	pd := wrapErr(ErrPermissionDenied, "doubao: bad key")
	if !errors.Is(pd, ErrPermissionDenied) {
		t.Fatal("wrapped ErrPermissionDenied must match via errors.Is")
	}
	if errors.Is(pd, ErrTransient) {
		t.Fatal("permission-denied error must not match ErrTransient")
	}
}

func TestTaskStatusValues(t *testing.T) {
	t.Parallel()
	cases := map[TaskStatus]string{
		StatusQueued:    "queued",
		StatusRunning:   "running",
		StatusSucceeded: "succeeded",
		StatusFailed:    "failed",
		StatusExpired:   "expired",
		StatusCancelled: "cancelled",
		StatusNotFound:  "not_found",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Fatalf("status %q != %q", got, want)
		}
	}
}
