package failover

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/mock"
	"harnessclaw-go/internal/provider/retry"
)

// drain consumes the stream's events and returns the terminal error.
func drain(t *testing.T, s *provider.ChatStream) error {
	t.Helper()
	for range s.Events {
	}
	return s.Err()
}

func successResponse() mock.Response {
	return mock.Response{Text: "ok", StopReason: "end_turn"}
}

// newForTest constructs a *Failover with the given providers and
// short cooldowns so tests run fast.
func newForTest(t *testing.T, provs ...provider.Provider) *Failover {
	t.Helper()
	fo, err := New(Config{
		Providers:      provs,
		CooldownBase:   30 * time.Second,
		CooldownMax:    5 * time.Minute,
		CooldownFactor: 2,
		Logger:         zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("failover.New err = %v", err)
	}
	return fo
}

func TestFailover_PrimarySuccessNoFailover(t *testing.T) {
	primary := mock.New(successResponse())
	fallback := mock.New(successResponse())
	fo := newForTest(t, primary, fallback)

	stream, err := fo.Chat(context.Background(), &provider.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if drainErr := drain(t, stream); drainErr != nil {
		t.Fatalf("stream err = %v", drainErr)
	}
	if primary.CallCount() != 1 {
		t.Fatalf("primary calls = %d, want 1", primary.CallCount())
	}
	if fallback.CallCount() != 0 {
		t.Fatalf("fallback should not have been called, got %d", fallback.CallCount())
	}
}

func TestFailover_PrimarySyncErrorAdvancesToFallback(t *testing.T) {
	primary := mock.New(mock.Response{Error: &retry.APIError{Type: retry.ErrServerError, Message: "5xx"}})
	fallback := mock.New(successResponse())
	fo := newForTest(t, primary, fallback)

	stream, err := fo.Chat(context.Background(), &provider.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if drainErr := drain(t, stream); drainErr != nil {
		t.Fatalf("stream err = %v", drainErr)
	}
	if primary.CallCount() != 1 || fallback.CallCount() != 1 {
		t.Fatalf("calls primary=%d fallback=%d, want 1/1", primary.CallCount(), fallback.CallCount())
	}
}

func TestFailover_NonFailoverWorthyErrorDoesNotAdvance(t *testing.T) {
	primary := mock.New(mock.Response{Error: &retry.APIError{Type: retry.ErrPromptTooLong, Message: "413"}})
	fallback := mock.New(successResponse())
	fo := newForTest(t, primary, fallback)

	_, err := fo.Chat(context.Background(), &provider.ChatRequest{Model: "test"})
	if err == nil {
		t.Fatalf("Chat err = nil, want prompt_too_long error")
	}
	if primary.CallCount() != 1 || fallback.CallCount() != 0 {
		t.Fatalf("calls primary=%d fallback=%d, want 1/0", primary.CallCount(), fallback.CallCount())
	}
}

func TestFailover_AllProvidersDownReturnsAggregateError(t *testing.T) {
	primary := mock.New(mock.Response{Error: &retry.APIError{Type: retry.ErrServerError, Message: "5xx-A"}})
	fallback := mock.New(mock.Response{Error: &retry.APIError{Type: retry.ErrServerError, Message: "5xx-B"}})
	fo := newForTest(t, primary, fallback)

	_, err := fo.Chat(context.Background(), &provider.ChatRequest{Model: "test"})
	if err == nil {
		t.Fatalf("Chat err = nil, want all-down error")
	}
	if !errors.Is(err, ErrAllProvidersDown) {
		t.Fatalf("err should wrap ErrAllProvidersDown, got %v", err)
	}
}

func TestFailover_TripsPrimaryUntilCooldownExpires(t *testing.T) {
	primary := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError, Message: "5xx"}},
		successResponse(),
		successResponse(),
	)
	fallback := mock.New(successResponse(), successResponse(), successResponse())
	fo := newForTest(t, primary, fallback)

	now := time.Unix(1_000_000, 0)
	fo.now = func() time.Time { return now }

	// Call 1: primary fails sync → trip → fallback succeeds.
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 1 err = %v", err)
	}
	if primary.CallCount() != 1 || fallback.CallCount() != 1 {
		t.Fatalf("after call 1: primary=%d fallback=%d, want 1/1", primary.CallCount(), fallback.CallCount())
	}

	// Call 2 inside cooldown: skip primary, hit fallback directly.
	now = now.Add(5 * time.Second)
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 2 err = %v", err)
	}
	if primary.CallCount() != 1 {
		t.Fatalf("primary should still be tripped (count=%d, want 1)", primary.CallCount())
	}
	if fallback.CallCount() != 2 {
		t.Fatalf("fallback count = %d, want 2", fallback.CallCount())
	}

	// Call 3 after cooldown: probe primary; succeeds; recover.
	now = now.Add(31 * time.Second)
	stream, err := fo.Chat(context.Background(), &provider.ChatRequest{})
	if err != nil {
		t.Fatalf("call 3 err = %v", err)
	}
	if drainErr := drain(t, stream); drainErr != nil {
		t.Fatalf("call 3 stream err = %v", drainErr)
	}
	if primary.CallCount() != 2 {
		t.Fatalf("primary should have been probed (count=%d, want 2)", primary.CallCount())
	}

	// After recover, call 4 routes back to primary directly.
	now = now.Add(1 * time.Second)
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 4 err = %v", err)
	}
	if primary.CallCount() != 3 {
		t.Fatalf("primary should have been picked first after recovery (count=%d, want 3)", primary.CallCount())
	}
}

func TestFailover_ProbeFailureExtendsCooldown(t *testing.T) {
	primary := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}}, // call 1: trip
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}}, // probe fails
	)
	fallback := mock.New(successResponse(), successResponse())
	fo := newForTest(t, primary, fallback)

	now := time.Unix(0, 0)
	fo.now = func() time.Time { return now }

	// Call 1 → trip, base cooldown 30s.
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 1 err = %v", err)
	}
	s := fo.state_(0)
	if s.cooldown() != 30*time.Second {
		t.Fatalf("first trip cooldown = %v, want 30s", s.cooldown())
	}

	// Skip ahead 31s, probe primary, probe fails → re-trip with 60s.
	now = now.Add(31 * time.Second)
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 2 err = %v", err)
	}
	s = fo.state_(0)
	if s.cooldown() != 60*time.Second {
		t.Fatalf("re-trip cooldown = %v, want 60s", s.cooldown())
	}
}

func TestFailover_AllTrippedTier3PicksEarliestRecovery(t *testing.T) {
	// Trip both then ask for one more call — Tier 3 fires.
	primary := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
	)
	fallback := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
	)
	fo := newForTest(t, primary, fallback)
	now := time.Unix(0, 0)
	fo.now = func() time.Time { return now }

	// Call 1 trips both providers in the same call (Tier 1 → Tier 1).
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err == nil {
		t.Fatalf("call 1 err = nil, want all-down")
	}

	// Both tripped. Step forward inside their cooldown (no provider eligible
	// in Tier 1 or Tier 2). Tier 3 must pick one.
	// trippedUntil: primary @ 30s, fallback @ 30s. With identical timestamps,
	// pick returns the first iteration order match — primary.
	now = now.Add(1 * time.Second)

	var picked []int
	fo.pickHook = func(idx int, _ string, _ RetryPolicy, _ bool) {
		picked = append(picked, idx)
	}
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err == nil {
		t.Fatalf("call 2 err = nil, want all-down")
	}
	if len(picked) == 0 {
		t.Fatalf("pickHook should have fired at least once on Tier 3 selection")
	}
	// Tier 3 must have selected at least one provider despite all being tripped.
	if primary.CallCount() < 2 && fallback.CallCount() < 3 {
		t.Fatalf("Tier 3 should have hit at least one provider; primary=%d fallback=%d",
			primary.CallCount(), fallback.CallCount())
	}
}

// --- Policy assignment tests via pickHook -----------------------------------

func TestFailover_PolicyAssignment_AllHealthyChoosesFast(t *testing.T) {
	primary := mock.New(successResponse())
	fallback := mock.New(successResponse())
	fo := newForTest(t, primary, fallback)

	var seen []RetryPolicy
	fo.pickHook = func(_ int, _ string, p RetryPolicy, _ bool) {
		seen = append(seen, p)
	}
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if len(seen) != 1 || seen[0].Name != "fast" {
		t.Fatalf("policies = %+v, want one Fast", seen)
	}
}

func TestFailover_PolicyAssignment_LastHealthyChoosesMedium(t *testing.T) {
	// Trip primary first; then on next call, only fallback is Healthy →
	// fallback should be picked with MediumPolicy.
	primary := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
		successResponse(),
	)
	fallback := mock.New(successResponse(), successResponse())
	fo := newForTest(t, primary, fallback)
	now := time.Unix(0, 0)
	fo.now = func() time.Time { return now }

	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 1 err = %v", err)
	}

	// Now within cooldown — only fallback is Healthy.
	now = now.Add(5 * time.Second)
	var seen []RetryPolicy
	fo.pickHook = func(_ int, _ string, p RetryPolicy, _ bool) {
		seen = append(seen, p)
	}
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 2 err = %v", err)
	}
	if len(seen) != 1 || seen[0].Name != "medium" {
		t.Fatalf("policies = %+v, want one Medium (last Healthy)", seen)
	}
}

func TestFailover_PolicyAssignment_ProbingUsesProbe(t *testing.T) {
	primary := mock.New(
		mock.Response{Error: &retry.APIError{Type: retry.ErrServerError}},
		successResponse(),
	)
	fallback := mock.New(successResponse(), successResponse())
	fo := newForTest(t, primary, fallback)
	now := time.Unix(0, 0)
	fo.now = func() time.Time { return now }

	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 1 err = %v", err)
	}

	// Advance past cooldown — primary becomes probe candidate.
	now = now.Add(31 * time.Second)
	// First pick: a Healthy fallback is still preferred over a probe.
	// To force the probe path we must temporarily mark fallback unavailable
	// — easiest: trip it manually via state_.
	fo.state_(1).trip(now)
	now = now.Add(1 * time.Second)

	var seen []RetryPolicy
	var sawProbing bool
	fo.pickHook = func(_ int, _ string, p RetryPolicy, probing bool) {
		seen = append(seen, p)
		if probing {
			sawProbing = true
		}
	}
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("call 2 err = %v", err)
	}
	if !sawProbing {
		t.Fatalf("expected at least one probing pick, seen=%+v", seen)
	}
	foundProbe := false
	for _, p := range seen {
		if p.Name == "probe" {
			foundProbe = true
			break
		}
	}
	if !foundProbe {
		t.Fatalf("policies = %+v, expected a Probe entry", seen)
	}
}

// --- Single-provider behavior -----------------------------------------------

func TestFailover_SingleProviderChainStillWorks(t *testing.T) {
	primary := mock.New(successResponse(), successResponse())
	fo := newForTest(t, primary)

	stream, err := fo.Chat(context.Background(), &provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if drainErr := drain(t, stream); drainErr != nil {
		t.Fatalf("stream err = %v", drainErr)
	}

	// Second call uses MediumPolicy (it's the only-Healthy provider).
	var seen []RetryPolicy
	fo.pickHook = func(_ int, _ string, p RetryPolicy, _ bool) {
		seen = append(seen, p)
	}
	if _, err := fo.Chat(context.Background(), &provider.ChatRequest{}); err != nil {
		t.Fatalf("Chat 2 err = %v", err)
	}
	if len(seen) != 1 || seen[0].Name != "medium" {
		t.Fatalf("single-provider chain should always use Medium, seen=%+v", seen)
	}
}

func TestFailover_New_RejectsEmptyChain(t *testing.T) {
	_, err := New(Config{Providers: nil})
	if err == nil {
		t.Fatalf("New with empty providers should error")
	}
}

// --- budgetTracker -----------------------------------------------------------

func TestArmBudget_ZeroBudgetReturnsNilTracker(t *testing.T) {
	ctx := context.Background()
	derived, bt := armBudget(ctx, 0)
	if bt != nil {
		t.Fatalf("zero budget should return nil tracker, got %+v", bt)
	}
	if derived != ctx {
		t.Fatalf("zero budget should return same ctx unchanged")
	}
}

func TestArmBudget_DisarmStopsTimer(t *testing.T) {
	ctx := context.Background()
	derived, bt := armBudget(ctx, 50*time.Millisecond)
	bt.disarm()
	time.Sleep(100 * time.Millisecond)
	if derived.Err() != nil {
		t.Fatalf("after disarm, ctx should not be cancelled, got %v", derived.Err())
	}
}

func TestArmBudget_TimerFiresOnExpiry(t *testing.T) {
	ctx := context.Background()
	derived, _ := armBudget(ctx, 20*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if derived.Err() == nil {
		t.Fatalf("after budget expiry, ctx should be cancelled")
	}
	if cause := context.Cause(derived); cause == nil {
		t.Fatalf("ctx cancel should carry a budget-exceeded cause")
	}
}
