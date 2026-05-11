package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/pkg/types"
)

// retryMockProvider drives retryLLMCall with a programmable failure
// sequence. Each Chat call returns either failErr (typed or string,
// caller picks) or a successful one-text-event stream.
type retryMockProvider struct {
	calls     int
	failUntil int   // fail the first N calls
	failErr   error // error returned when failing
}

func (p *retryMockProvider) Name() string { return "retry-mock" }

func (p *retryMockProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	p.calls++
	if p.calls <= p.failUntil {
		return nil, p.failErr
	}
	ch := make(chan types.StreamEvent, 3)
	go func() {
		defer close(ch)
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "ok"}
		ch <- types.StreamEvent{
			Type:       types.StreamEventMessageEnd,
			StopReason: "end_turn",
			Usage:      &types.Usage{InputTokens: 10, OutputTokens: 5},
		}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func (p *retryMockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 100, nil
}

// fastRetryer builds a Retryer tuned for tests: short delays so the
// suite finishes quickly, no jitter so attempt counts are stable.
func fastRetryer(maxRetries int) *retry.Retryer {
	cfg := &retry.Config{
		MaxRetries:       maxRetries,
		InitialDelay:     1 * time.Millisecond,
		MaxDelay:         5 * time.Millisecond,
		JitterFraction:   0,
		FallbackAfter529: 3,
	}
	return retry.New(cfg, zap.NewNop())
}

func TestRetryLLMCall_SuccessOnFirstAttempt(t *testing.T) {
	prov := &retryMockProvider{}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(3), llmCallTimeouts{}, "", nil)

	if result.streamErr != nil {
		t.Fatalf("unexpected error: %v", result.streamErr)
	}
	if result.textBuf != "ok" {
		t.Errorf("expected text 'ok', got %q", result.textBuf)
	}
	if prov.calls != 1 {
		t.Errorf("expected 1 call, got %d", prov.calls)
	}
}

func TestRetryLLMCall_SuccessAfterRetry(t *testing.T) {
	// Untyped network-class error → toAPIError wraps as ClassifyNetworkError
	// (Retryable=true), so the Retryer keeps trying.
	prov := &retryMockProvider{
		failUntil: 2,
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(3), llmCallTimeouts{}, "", nil)

	if result.streamErr != nil {
		t.Fatalf("unexpected error: %v", result.streamErr)
	}
	if result.textBuf != "ok" {
		t.Errorf("expected text 'ok', got %q", result.textBuf)
	}
	if prov.calls != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", prov.calls)
	}
}

// Exhaustion contract: with MaxRetries=N, total attempts = N+1.
func TestRetryLLMCall_ExhaustsRetries(t *testing.T) {
	const maxRetries = 3
	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(maxRetries), llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if prov.calls != maxRetries+1 {
		t.Errorf("expected %d calls (1 initial + %d retries), got %d",
			maxRetries+1, maxRetries, prov.calls)
	}
}

// HTTP 4xx errors classified as non-retryable abort after the first
// attempt, even when many retries are configured.
func TestRetryLLMCall_NonRetryable_StatusCode(t *testing.T) {
	apiErr := retry.ClassifyHTTPError(401, "auth failed", errors.New("auth failed"))
	if apiErr.Retryable {
		t.Fatal("setup: 401 should classify as non-retryable")
	}
	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   apiErr,
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(5), llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error for non-retryable failure")
	}
	if prov.calls != 1 {
		t.Errorf("expected 1 call (no retry for 401); got %d", prov.calls)
	}
}

// HTTP 503 from the provider should retry up to MaxRetries.
func TestRetryLLMCall_RetryableHTTP_RetriesAndExhausts(t *testing.T) {
	const maxRetries = 2
	apiErr := retry.ClassifyHTTPError(503, "service unavailable", errors.New("503"))
	if !apiErr.Retryable {
		t.Fatal("setup: 503 should classify as retryable")
	}
	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   apiErr,
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(maxRetries), llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error after exhausting retries on persistent 503")
	}
	if prov.calls != maxRetries+1 {
		t.Errorf("expected %d calls; got %d", maxRetries+1, prov.calls)
	}
}

// Three consecutive 529s must surface a *FallbackTriggeredError so the
// engine can react (log it, switch fallback model, etc.) instead of
// silently retrying forever.
func TestRetryLLMCall_Consecutive529TriggersFallback(t *testing.T) {
	apiErr := retry.ClassifyHTTPError(529, "overloaded", errors.New("529"))
	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   apiErr,
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(10), llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error after 529 fallback trigger")
	}
	var fbErr *retry.FallbackTriggeredError
	if !errors.As(result.streamErr, &fbErr) {
		t.Errorf("expected FallbackTriggeredError; got %T: %v",
			result.streamErr, result.streamErr)
	}
	// Default FallbackAfter529=3, so we should see exactly 3 calls
	// before the trigger fires.
	if prov.calls != 3 {
		t.Errorf("expected 3 calls before fallback trigger; got %d", prov.calls)
	}
}

// Cancelled context aborts retries promptly without the underlying
// provider being asked another time.
func TestRetryLLMCall_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	result := retryLLMCall(ctx, prov, &provider.ChatRequest{}, zap.NewNop(), fastRetryer(5), llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// nil retryer is a defensive no-op — single attempt, no retries. Lets
// callers skip retry plumbing in tight integration tests without
// blocking on retry delays.
func TestRetryLLMCall_NilRetryer_SingleAttempt(t *testing.T) {
	prov := &retryMockProvider{
		failUntil: 100,
		failErr:   fmt.Errorf("transient"),
	}
	result := retryLLMCall(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(), nil, llmCallTimeouts{}, "", nil)

	if result.streamErr == nil {
		t.Fatal("expected error to surface")
	}
	if prov.calls != 1 {
		t.Errorf("nil retryer should attempt exactly once; got %d", prov.calls)
	}
}

// hangingProvider returns a Chat stream that NEVER sends a chunk and
// NEVER closes — until ctx is cancelled, at which point it closes the
// channel and surfaces ctx.Err() through stream.Err. Models the "TCP
// black hole" pathology that motivated FirstByteTimeout: gateway
// accepts the request, opens an HTTP body, then disappears.
type hangingProvider struct {
	calls int
}

func (p *hangingProvider) Name() string { return "hanging-mock" }

func (p *hangingProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func (p *hangingProvider) Chat(ctx context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	p.calls++
	ch := make(chan types.StreamEvent)
	var streamErr error
	go func() {
		defer close(ch)
		<-ctx.Done()
		streamErr = ctx.Err()
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return streamErr }}, nil
}

// TestRetryLLMCall_FirstByteTimeoutCancelsHungCall is the regression
// guard for the "10-min orphan_timeout on a step that never produced
// any event" incident. With FirstByteTimeout configured, a hung Chat
// call must be interrupted within the budget; the resulting error
// surfaces errFirstByteTimeout (or a wrapped form) so retry / logs see
// the specific upstream-stall reason.
func TestRetryLLMCall_FirstByteTimeoutCancelsHungCall(t *testing.T) {
	prov := &hangingProvider{}
	timeouts := llmCallTimeouts{firstByte: 30 * time.Millisecond}

	start := time.Now()
	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0), // no retries — exercise the fail-fast path
		timeouts,
		"",
		nil,
	)
	elapsed := time.Since(start)

	if result.streamErr == nil {
		t.Fatal("expected first-byte watchdog to surface an error")
	}
	if !errors.Is(result.streamErr, errFirstByteTimeout) {
		t.Errorf("error should wrap errFirstByteTimeout; got %v", result.streamErr)
	}
	// Should fire well under 1s — budget is 30ms.
	if elapsed > 500*time.Millisecond {
		t.Errorf("call took %v; expected <500ms with 30ms first-byte budget", elapsed)
	}
}

// TestRetryLLMCall_FirstByteWatchdogDisarmsAfterFirstChunk pins the
// "no false positives" half of the contract: once the first chunk
// arrives the watchdog must stop firing, even on streams that legit
// take longer than the FirstByteTimeout budget overall.
func TestRetryLLMCall_FirstByteWatchdogDisarmsAfterFirstChunk(t *testing.T) {
	// Provider sends one chunk immediately, then sleeps 100ms before
	// MessageEnd. With firstByte=30ms, naive impl would cancel during
	// the 100ms wait — correct impl disarms after the first chunk.
	prov := &slowAfterFirstByteProvider{}
	timeouts := llmCallTimeouts{firstByte: 30 * time.Millisecond}

	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0),
		timeouts,
		"",
		nil,
	)
	if result.streamErr != nil {
		t.Fatalf("watchdog should have disarmed; got error %v", result.streamErr)
	}
	if result.textBuf != "ok" {
		t.Errorf("expected text 'ok'; got %q", result.textBuf)
	}
}

// slowAfterFirstByteProvider sends one chunk fast, then waits before
// MessageEnd. Exercises the "long thinking after start" pattern that
// FirstByteTimeout must NOT penalise.
type slowAfterFirstByteProvider struct{}

func (p *slowAfterFirstByteProvider) Name() string { return "slow-after-first" }
func (p *slowAfterFirstByteProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (p *slowAfterFirstByteProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 2)
	go func() {
		defer close(ch)
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "ok"}
		time.Sleep(100 * time.Millisecond) // longer than first-byte budget
		ch <- types.StreamEvent{Type: types.StreamEventMessageEnd, StopReason: "end_turn"}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

// TestRetryLLMCall_APITimeoutCutsLongStream pins API total deadline:
// even when the stream is producing chunks, total wall-clock cap kicks
// in. Catches "stream that trickles forever".
func TestRetryLLMCall_APITimeoutCutsLongStream(t *testing.T) {
	prov := &trickleProvider{}
	timeouts := llmCallTimeouts{api: 50 * time.Millisecond}

	start := time.Now()
	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0),
		timeouts,
		"",
		nil,
	)
	elapsed := time.Since(start)

	if result.streamErr == nil {
		t.Fatal("expected API timeout to surface error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("call took %v; expected ~50ms with API budget 50ms", elapsed)
	}
}

// trickleProvider sends one chunk every 20ms forever — exercises the
// "stream is alive but taking too long overall" path that
// FirstByteTimeout doesn't catch.
type trickleProvider struct{}

func (p *trickleProvider) Name() string { return "trickle" }
func (p *trickleProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (p *trickleProvider) Chat(ctx context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent)
	var streamErr error
	go func() {
		defer close(ch)
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				streamErr = ctx.Err()
				return
			case <-t.C:
				select {
				case ch <- types.StreamEvent{Type: types.StreamEventText, Text: "."}:
				case <-ctx.Done():
					streamErr = ctx.Err()
					return
				}
			}
		}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return streamErr }}, nil
}

// slowFirstByteProvider holds the stream open without sending any
// chunk until ctx fires OR doneAfter elapses, then sends one chunk
// and closes. Lets us simulate "LLM call sits silent for a while then
// produces output" — the scenario that needs heartbeats.
type slowFirstByteProvider struct {
	silentFor time.Duration
}

func (p *slowFirstByteProvider) Name() string { return "slow-first-byte" }
func (p *slowFirstByteProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (p *slowFirstByteProvider) Chat(ctx context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 2)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.silentFor):
		}
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "ok"}
		ch <- types.StreamEvent{Type: types.StreamEventMessageEnd, StopReason: "end_turn"}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

// TestRetryLLMCall_EmitsHeartbeatsDuringSilentWait pins the contract
// that motivates this whole patch: while the LLM call is in flight
// without chunks, the engine emits periodic EngineEventLLMHeartbeat
// events on `out`. Without them, the surrounding step / agent cards
// hit OrphanTimeoutMs (5min / 10min) on slow upstreams and watchdog-
// kill perfectly healthy work — see the production incident with
// Xunfei "Engine Busy" responses returning after 2m12s.
func TestRetryLLMCall_EmitsHeartbeatsDuringSilentWait(t *testing.T) {
	// Override the heartbeat interval to something fast so the test
	// finishes quickly. The package-level const is `llmHeartbeatInterval`
	// — we can't change it from the test without exporting a setter,
	// so the test uses a provider that stays silent for ~100ms and
	// asserts at least one heartbeat fires. Adjust the interval if
	// the production default ever drops below 100ms (currently 30s).
	prev := llmHeartbeatIntervalForTest()
	setLLMHeartbeatIntervalForTest(20 * time.Millisecond)
	defer setLLMHeartbeatIntervalForTest(prev)

	prov := &slowFirstByteProvider{silentFor: 80 * time.Millisecond}
	out := make(chan types.EngineEvent, 16)

	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0),
		llmCallTimeouts{}, // no first-byte timeout — we want the wait to actually happen
		"agent_test123",
		out,
	)
	close(out)

	if result.streamErr != nil {
		t.Fatalf("call should succeed; got %v", result.streamErr)
	}

	var heartbeats []types.EngineEvent
	for ev := range out {
		if ev.Type == types.EngineEventLLMHeartbeat {
			heartbeats = append(heartbeats, ev)
		}
	}
	if len(heartbeats) < 2 {
		t.Fatalf("expected ≥2 heartbeats over 80ms with 20ms interval; got %d", len(heartbeats))
	}
	for i, hb := range heartbeats {
		if hb.AgentID != "agent_test123" {
			t.Errorf("heartbeat %d agent_id = %q, want agent_test123", i, hb.AgentID)
		}
		if hb.Duration <= 0 {
			t.Errorf("heartbeat %d Duration_ms = %d, want > 0", i, hb.Duration)
		}
	}
}

// TestRetryLLMCall_HeartbeatsStopAfterCompletion pins: no heartbeats
// fire after the LLM call resolves. Otherwise a fast call would leak
// goroutines / clog the out channel with no-op ticks.
func TestRetryLLMCall_HeartbeatsStopAfterCompletion(t *testing.T) {
	prev := llmHeartbeatIntervalForTest()
	setLLMHeartbeatIntervalForTest(10 * time.Millisecond)
	defer setLLMHeartbeatIntervalForTest(prev)

	prov := &retryMockProvider{} // first attempt succeeds immediately
	out := make(chan types.EngineEvent, 16)

	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0),
		llmCallTimeouts{},
		"agent_done",
		out,
	)
	if result.streamErr != nil {
		t.Fatalf("expected success; got %v", result.streamErr)
	}

	// Sleep a few intervals — any extra heartbeats fired here would
	// leak into the channel.
	time.Sleep(40 * time.Millisecond)
	close(out)

	hbCount := 0
	for ev := range out {
		if ev.Type == types.EngineEventLLMHeartbeat {
			hbCount++
		}
	}
	if hbCount > 1 {
		t.Errorf("fast success should not emit multiple heartbeats; got %d", hbCount)
	}
}

// twoChunkProvider sends two text chunks plus one tool_use, mimicking
// a typical Anthropic-style tool-using response. Used to verify the
// buffer-then-replay path emits exactly the same wire events as if
// the chunks streamed live, just in a single burst after the call
// resolves.
type twoChunkProvider struct{}

func (p *twoChunkProvider) Name() string { return "two-chunk" }
func (p *twoChunkProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (p *twoChunkProvider) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, 4)
	go func() {
		defer close(ch)
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "Hello "}
		ch <- types.StreamEvent{Type: types.StreamEventText, Text: "world"}
		ch <- types.StreamEvent{
			Type:     types.StreamEventToolUse,
			ToolCall: &types.ToolCall{ID: "tu_a", Name: "Bash", Input: `{"command":"ls"}`},
		}
		ch <- types.StreamEvent{Type: types.StreamEventMessageEnd, StopReason: "tool_use"}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

// TestRetryLLMCall_ReplaysBufferedContentOnSuccess pins the Plan-B
// contract: every attempt is buffered (no live streaming), and the
// successful attempt's full content is replayed onto out as a single
// burst — text concatenated, then tool_uses in arrival order.
//
// Why this matters: in the old design, attempt 1 streamed text live.
// If attempt 1 failed mid-stream and a retry succeeded, the wire had
// stale partial content from attempt 1, while the engine's internal
// model state held attempt 2's fresh full answer. Buffer-then-replay
// guarantees wire = internal state.
func TestRetryLLMCall_ReplaysBufferedContentOnSuccess(t *testing.T) {
	out := make(chan types.EngineEvent, 16)

	result := retryLLMCall(
		context.Background(),
		&twoChunkProvider{},
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(0),
		llmCallTimeouts{},
		"agent_replay",
		out,
	)
	close(out)

	if result.streamErr != nil {
		t.Fatalf("call should succeed; got %v", result.streamErr)
	}
	if result.textBuf != "Hello world" {
		t.Errorf("textBuf mismatch: %q", result.textBuf)
	}
	if len(result.toolCalls) != 1 || result.toolCalls[0].Name != "Bash" {
		t.Errorf("toolCalls mismatch: %+v", result.toolCalls)
	}

	var (
		texts   []string
		tools   []string
		hbCount int
	)
	for ev := range out {
		switch ev.Type {
		case types.EngineEventText:
			texts = append(texts, ev.Text)
		case types.EngineEventToolUse:
			tools = append(tools, ev.ToolName)
		case types.EngineEventLLMHeartbeat:
			hbCount++
		default:
			t.Errorf("unexpected event type on wire: %s", ev.Type)
		}
	}
	// Buffer-then-replay: exactly one EngineEventText carrying the
	// CONCATENATED textBuf (not one event per chunk like live
	// streaming would produce).
	if len(texts) != 1 || texts[0] != "Hello world" {
		t.Errorf("expected exactly one text event with full content; got %v", texts)
	}
	if len(tools) != 1 || tools[0] != "Bash" {
		t.Errorf("expected one tool_use; got %v", tools)
	}
	// Heartbeats may or may not fire (call is fast); not asserting.
	_ = hbCount
}

// TestRetryLLMCall_NoEmissionOnFailure pins the failure half of the
// contract: when retries are exhausted, NO text/tool_use events fire
// onto out at all. The caller (subagent_driver / queryloop) is
// responsible for emitting Error / MessageDelta(stop=error) /
// MessageStop frames. retryLLMCall stays out of the way.
func TestRetryLLMCall_NoEmissionOnFailure(t *testing.T) {
	const maxRetries = 2
	prov := &retryMockProvider{
		failUntil: 100, // every attempt fails
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}
	out := make(chan types.EngineEvent, 16)
	result := retryLLMCall(
		context.Background(),
		prov,
		&provider.ChatRequest{},
		zap.NewNop(),
		fastRetryer(maxRetries),
		llmCallTimeouts{},
		"agent_fail",
		out,
	)
	close(out)

	if result.streamErr == nil {
		t.Fatal("call should have failed")
	}
	if prov.calls != maxRetries+1 {
		t.Errorf("expected %d attempts; got %d", maxRetries+1, prov.calls)
	}

	// Only status ticks (heartbeats / retry notes) should appear on
	// out — never text / tool_use / error frames from retryLLMCall
	// itself. The caller emits its own error envelope after we return.
	for ev := range out {
		switch ev.Type {
		case types.EngineEventLLMHeartbeat:
			// fine — keep-alive ticks during the wait
		case types.EngineEventLLMRetry:
			// fine — status tick fired by retry.Retryer before each
			// backoff sleep. Carries no assistant content; it's the
			// "we're retrying" wire signal the front-end needs to
			// distinguish backoff from a slow upstream.
		case types.EngineEventText, types.EngineEventToolUse, types.EngineEventError:
			t.Errorf("retryLLMCall must not emit %s on failure path", ev.Type)
		default:
			t.Errorf("unexpected wire event: %s", ev.Type)
		}
	}
}

// toAPIError must pass typed errors through and wrap untyped ones as
// retryable network errors. This is the contract that lets the
// transitional period (some providers typed, some not) work without
// dropping retries.
func TestToAPIError_ClassificationContract(t *testing.T) {
	t.Run("nil error → nil", func(t *testing.T) {
		if got := toAPIError(nil); got != nil {
			t.Errorf("toAPIError(nil) = %v, want nil", got)
		}
	})
	t.Run("typed APIError passes through", func(t *testing.T) {
		want := retry.ClassifyHTTPError(429, "rate limit", errors.New("429"))
		got := toAPIError(want)
		if got != want {
			t.Errorf("typed APIError should pass through unchanged; got %v", got)
		}
	})
	t.Run("untyped error wraps as retryable network error", func(t *testing.T) {
		got := toAPIError(errors.New("some weird upstream issue"))
		if got == nil {
			t.Fatal("untyped error should wrap, not return nil")
		}
		if !got.Retryable {
			t.Errorf("untyped errors should default to retryable to avoid dropping transients")
		}
		if got.Type != retry.ErrNetworkError {
			t.Errorf("untyped error type = %v, want %v", got.Type, retry.ErrNetworkError)
		}
	})
}
