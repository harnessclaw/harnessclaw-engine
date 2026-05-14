package failover

import (
	"errors"
	"testing"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/pkg/types"
)

func TestWrapStream_SuccessCallsOnSuccess(t *testing.T) {
	events := make(chan types.StreamEvent)
	close(events)
	inner := &provider.ChatStream{
		Events: events,
		Err:    func() error { return nil },
	}
	var onSuccess, onFailure int
	wrapped := wrapStream(inner,
		func() { onSuccess++ },
		func(err error) { onFailure++ },
	)
	for range wrapped.Events {
	}
	if got := wrapped.Err(); got != nil {
		t.Fatalf("Err = %v, want nil", got)
	}
	if onSuccess != 1 || onFailure != 0 {
		t.Fatalf("onSuccess=%d onFailure=%d, want 1/0", onSuccess, onFailure)
	}
}

func TestWrapStream_FailoverWorthyTerminalCallsOnFailure(t *testing.T) {
	events := make(chan types.StreamEvent)
	close(events)
	apiErr := &retry.APIError{Type: retry.ErrServerError, Message: "boom"}
	inner := &provider.ChatStream{
		Events: events,
		Err:    func() error { return apiErr },
	}
	var onSuccess, onFailure int
	var captured error
	wrapped := wrapStream(inner,
		func() { onSuccess++ },
		func(err error) { onFailure++; captured = err },
	)
	for range wrapped.Events {
	}
	if got := wrapped.Err(); !errors.Is(got, apiErr) {
		t.Fatalf("Err = %v, want %v", got, apiErr)
	}
	if onSuccess != 0 || onFailure != 1 {
		t.Fatalf("onSuccess=%d onFailure=%d, want 0/1", onSuccess, onFailure)
	}
	if !errors.Is(captured, apiErr) {
		t.Fatalf("captured=%v, want %v", captured, apiErr)
	}
}

func TestWrapStream_NonFailoverWorthyTerminalSkipsCallbacks(t *testing.T) {
	events := make(chan types.StreamEvent)
	close(events)
	apiErr := &retry.APIError{Type: retry.ErrPromptTooLong, Message: "too long"}
	inner := &provider.ChatStream{
		Events: events,
		Err:    func() error { return apiErr },
	}
	var onSuccess, onFailure int
	wrapped := wrapStream(inner,
		func() { onSuccess++ },
		func(err error) { onFailure++ },
	)
	for range wrapped.Events {
	}
	if got := wrapped.Err(); !errors.Is(got, apiErr) {
		t.Fatalf("Err = %v, want %v", got, apiErr)
	}
	if onSuccess != 0 || onFailure != 0 {
		t.Fatalf("non-failover-worthy terminal should call NEITHER callback; got success=%d failure=%d", onSuccess, onFailure)
	}
}

func TestWrapStream_IdempotentErr(t *testing.T) {
	events := make(chan types.StreamEvent)
	close(events)
	inner := &provider.ChatStream{
		Events: events,
		Err:    func() error { return nil },
	}
	var onSuccess int
	wrapped := wrapStream(inner,
		func() { onSuccess++ },
		func(err error) {},
	)
	for range wrapped.Events {
	}
	_ = wrapped.Err()
	_ = wrapped.Err()
	_ = wrapped.Err()
	if onSuccess != 1 {
		t.Fatalf("onSuccess fired %d times, want 1 (Err() may be called repeatedly)", onSuccess)
	}
}
