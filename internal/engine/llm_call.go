package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/pkg/types"
)

// llmHeartbeatInterval is how often callLLM pings the parent
// `out` channel with a card-tick keep-alive while the LLM call is
// in flight. Picked so that the surrounding step (5 min default
// OrphanTimeoutMs) and agent (10 min) cards see ~10× margin — even
// with 4× the interval missing in a row, the watchdog still won't
// fire. Adjust here if you raise either timeout.
//
// var (not const) so tests can swap to a small interval via
// setLLMHeartbeatIntervalForTest without forking the production
// code path. Production callers never mutate it.
var llmHeartbeatInterval = 30 * time.Second

// Test-only accessors. Keep here (not in _test.go) so they share the
// var with the runtime path and don't get build-tagged out.
func llmHeartbeatIntervalForTest() time.Duration   { return llmHeartbeatInterval }
func setLLMHeartbeatIntervalForTest(d time.Duration) { llmHeartbeatInterval = d }

// llmCallTimeouts bundles the per-attempt deadlines applied to one
// provider.Chat() round-trip. Zero values disable the corresponding
// guard.
type llmCallTimeouts struct {
	// API caps total wall-clock for one Chat() + stream consumption.
	// When the upstream returns the connection but trickles bytes
	// indefinitely (or never sends MessageEnd), this is the upper
	// bound that triggers retry / surfaces the failure.
	api time.Duration

	// FirstByte caps the silent gap between Chat() returning and the
	// first stream chunk arriving. Catches "TCP black hole" — gateway
	// accepted the request but never sends a byte. Disarmed once the
	// first chunk lands so legitimate slow streams (long thinking
	// preludes) aren't penalised.
	firstByte time.Duration
}

// errFirstByteTimeout is the typed cause we attach via WithCancelCause
// when the first-byte watchdog cancels the call. After the stream
// loop exits we check context.Cause to surface this — tests and the
// retry classifier branch on it instead of guessing from a generic
// ctx.Err().
var errFirstByteTimeout = errors.New("LLM call: no first byte within budget (upstream stall)")

// llmTimeouts pulls the per-call deadlines off the QueryEngine's
// config. Centralised so callers don't repeat the field plumbing —
// every callLLM call site is `qe.llmTimeouts()`.
func (qe *QueryEngine) llmTimeouts() llmCallTimeouts {
	if qe == nil {
		return llmCallTimeouts{}
	}
	return llmCallTimeouts{
		api:       qe.config.LLMAPITimeout,
		firstByte: qe.config.LLMFirstByteTimeout,
	}
}

// llmCallResult holds the outcome of a single LLM Chat + stream consumption attempt.
type llmCallResult struct {
	textBuf    string
	toolCalls  []types.ToolCall
	stopReason string
	lastUsage  *types.Usage
	// reasoning is the thinking-mode chain-of-thought captured on the
	// terminal MessageEnd event. Threaded onto the outgoing assistant
	// Message so the next request can echo it back (required by
	// DeepSeek thinking-mode models).
	reasoning string
	streamErr error // non-nil if the call or stream failed

	// Timing breakdown captured by callLLMOnce. All durations are
	// from the moment Chat() was invoked. Zero means "never observed".
	// Used to diagnose "elapsed is huge but frontend got the answer
	// quickly" — distinguishes gateway hangs (large endDelta), extended
	// thinking (large firstByte), and network buffering (anything in
	// between).
	firstByteAt time.Duration // first text/tool chunk arrived
	lastChunkAt time.Duration // last text/tool chunk arrived
	endAt       time.Duration // MessageEnd arrived
}

// callLLM attempts to call provider.Chat and consume the stream,
// driving the retry loop through retry.Retryer. The Retryer owns the
// backoff schedule, jitter, status-code-based retryability, and the
// consecutive-529 fallback signal — this function only handles the
// per-attempt streaming + timing instrumentation.
//
// When out is non-nil, events are streamed in real-time on the FIRST
// attempt (optimistic path). Retries buffer silently to avoid emitting
// duplicate / partial data; the caller's result already received the
// first attempt's stream so a clean overwrite isn't possible — but
// that's the same trade-off the previous bespoke loop made.
//
// retryer must be non-nil (callers fetch it from QueryEngine.retryer);
// nil retryer disables retries entirely (single-attempt fallback).
//
// Returns the result of the successful attempt, or the last failed
// attempt with streamErr populated. The wire-level error type
// (*retry.APIError or *retry.FallbackTriggeredError) is preserved on
// streamErr so callers / tests can inspect it.
// callLLM: see file-level doc. agentID, if non-empty, lands on
// every per-attempt heartbeat event so the channel translator can
// route it to the correct sub-agent card. Empty agentID = L1 main
// loop; heartbeats then target whatever card is the most-specific
// open card on the session.
func callLLM(
	ctx context.Context,
	prov provider.Provider,
	req *provider.ChatRequest,
	logger *zap.Logger,
	retryer *retry.Retryer,
	timeouts llmCallTimeouts,
	agentID string,
	out chan<- types.EngineEvent,
	planningOut chan<- types.EngineEvent,
) *llmCallResult {
	var result *llmCallResult
	attempt := 0

	logSubmissionShape(logger, req)

	doOnce := func(ctx context.Context) error {
		attempt++

		// All attempts are silently buffered — no text / tool_use is
		// streamed to `out` during the call. We replay the
		// SUCCESSFUL attempt's full content once retryer.Do returns
		// (see the post-loop block). Trade-off: front-end no longer
		// sees typing-style live text; in exchange there's never a
		// case where attempt 1 streams a partial response, fails, a
		// retry produces a different answer, and the user is stuck
		// with stale prefix on screen + a fresh full reply in the
		// model's internal state. Plan-mode sub-agents don't emit
		// text to the wire at all (L1/L2 isolation), so for them
		// this change is invisible.
		//
		// Heartbeats DO keep firing on `out` during the wait — they
		// don't carry assistant content, only watchdog-keepalive
		// ticks, so they're orthogonal to the buffer-vs-stream
		// trade-off.

		// Heartbeat: while this attempt is in flight, ping the parent
		// `out` channel every llmHeartbeatInterval with a typed
		// EngineEventLLMHeartbeat. The translator turns it into a
		// card.tick(kind=heartbeat) on the surrounding agent /
		// message card; that tick walks the parent chain via
		// Tracker.Touch and resets every ancestor's orphan deadline.
		//
		// Why: the LLM stream loop blocks waiting for the first
		// chunk. If the upstream gateway takes minutes to start
		// streaming (Xunfei "Engine Busy" returning at 2m12s; slow
		// reasoning models taking 1-3 min for first byte), zero
		// wire events fire for the duration — surrounding step / agent
		// cards see no activity, hit their OrphanTimeoutMs (5min /
		// 10min), and the watchdog synthesises a failed close even
		// though the work is still healthy.
		//
		// `out` is the parent's ParentOut for sub-agents (events go
		// up the spawn chain and through the L2→L1 forward) or the
		// channel's session event sink for L1. Either way the heartbeat
		// lands on the right session.
		// Ordered shutdown: hbDone signals the goroutine to stop;
		// hbExited blocks the deferred close until the goroutine
		// has actually returned. Without the wait, a caller that
		// closes `out` immediately after callLLM returns could
		// panic the heartbeat goroutine on a mid-tick send. In
		// production the wait is microseconds (the next select
		// iteration sees hbDone closed and returns); the safety
		// margin is what matters.
		hbDone := make(chan struct{})
		hbExited := make(chan struct{})
		if out != nil {
			go func() {
				defer close(hbExited)
				runLLMHeartbeat(ctx, hbDone, out, agentID, time.Now())
			}()
		} else {
			close(hbExited) // no goroutine started; let the wait be a no-op
		}
		defer func() {
			close(hbDone)
			<-hbExited
		}()

		startedAt := time.Now()
		logger.Info("llm.call begin",
			zap.Int("attempt", attempt),
			zap.Int("messages", len(req.Messages)),
			zap.Int("tools", len(req.Tools)),
			zap.Int("system_chars", len(req.System)),
			zap.Int("max_tokens", req.MaxTokens),
		)
		// Pass nil for out — every attempt is buffered, never streamed live.
		// See the comment block above for the rationale.
		// planningOut is threaded through so planning events fire live
		// even during retried attempts (and are retracted on each retry).
		attemptResult := callLLMOnce(ctx, prov, req, nil, planningOut, timeouts, logger)
		elapsed := time.Since(startedAt)

		if attemptResult.streamErr == nil {
			tailAfterLastChunk := elapsed - attemptResult.lastChunkAt
			if attemptResult.lastChunkAt == 0 {
				tailAfterLastChunk = 0
			}
			var inputTok, outputTok, cacheRead, cacheWrite, thinkingTok int64
			if attemptResult.lastUsage != nil {
				inputTok = int64(attemptResult.lastUsage.InputTokens)
				outputTok = int64(attemptResult.lastUsage.OutputTokens)
				cacheRead = int64(attemptResult.lastUsage.CacheRead)
				cacheWrite = int64(attemptResult.lastUsage.CacheWrite)
				thinkingTok = int64(attemptResult.lastUsage.ThinkingTokens)
			}
			logger.Info("llm.call ok",
				zap.Int("attempt", attempt),
				zap.Duration("elapsed", elapsed),
				zap.Duration("first_byte", attemptResult.firstByteAt),
				zap.Duration("last_chunk", attemptResult.lastChunkAt),
				zap.Duration("end_at", attemptResult.endAt),
				zap.Duration("tail_after_last_chunk", tailAfterLastChunk),
				zap.Int("text_chars", len(attemptResult.textBuf)),
				zap.Int("tool_calls", len(attemptResult.toolCalls)),
				zap.String("stop_reason", attemptResult.stopReason),
				zap.Int64("input_tokens", inputTok),
				zap.Int64("output_tokens", outputTok),
				zap.Int64("cache_read", cacheRead),
				zap.Int64("cache_write", cacheWrite),
				zap.Int64("thinking_tokens", thinkingTok),
			)
			if attempt > 1 {
				logger.Info("LLM call succeeded after retry",
					zap.Int("attempt", attempt),
				)
			}
			result = attemptResult
			return nil
		}

		logger.Warn("llm.call err",
			zap.Int("attempt", attempt),
			zap.Duration("elapsed", elapsed),
			zap.Error(attemptResult.streamErr),
		)
		result = attemptResult
		return toAPIError(attemptResult.streamErr)
	}

	// onRetry surfaces retry decisions to the wire. Fires once per
	// scheduled retry, just before the Retryer sleeps. Translator turns
	// each event into a card.tick(kind=note) on the active agent /
	// message card so the user sees "重试中 (2/10, 0.8s 后再试)" instead
	// of a silent stall. Without this, the front-end can't distinguish
	// "slow upstream" from "retrying after 5xx".
	//
	// Non-blocking send by design (matches the heartbeat path): if the
	// consumer is slow we'd rather drop the status tick than wedge the
	// retry loop.
	onRetry := func(attempt int, delay time.Duration, apiErr *retry.APIError) {
		if out == nil || apiErr == nil {
			return
		}
		info := &types.LLMRetryInfo{
			Attempt:    attempt,
			MaxRetries: retryer.MaxRetries(),
			DelayMs:    delay.Milliseconds(),
			ErrorType:  string(apiErr.Type),
			StatusCode: apiErr.StatusCode,
			Message:    truncForLog(apiErr.Message, 200),
		}
		select {
		case out <- types.EngineEvent{
			Type:     types.EngineEventLLMRetry,
			AgentID:  agentID,
			LLMRetry: info,
		}:
		default:
			// Buffer full — drop. The WARN log inside retry.Retryer
			// still records the event for post-hoc analysis.
		}

		// Tell translator that any planning-only tool cards opened during
		// the prior attempt's stream must be cancelled — the next attempt
		// will re-emit fresh ToolPlanning events with possibly different
		// tool identities.
		if planningOut != nil {
			select {
			case planningOut <- types.EngineEvent{
				Type:    types.EngineEventToolPlanningRetract,
				AgentID: agentID,
			}:
			default:
				// chan full — drop; the next attempt's planning events
				// still flow, and ToolStart upgrade will clean up the
				// toolsFromPlanning map even if Retract was missed.
			}
		}
	}

	var err error
	if retryer == nil {
		err = doOnce(ctx)
	} else {
		err = retryer.DoWith(ctx, doOnce, onRetry)
	}

	// Retryer.Do returns:
	//   - nil on success
	//   - *FallbackTriggeredError when consecutive 529s exhausted
	//   - the underlying *APIError on retry exhaustion or non-retryable
	//   - ctx.Err() on cancellation
	// The Retryer's terminal err is more informative than the
	// per-attempt err (e.g. "FallbackTriggered after 3x 529" vs the
	// final APIError{529}), so it always wins on result.streamErr.
	if err != nil {
		if result == nil {
			result = &llmCallResult{}
		}
		result.streamErr = err
		var fbErr *retry.FallbackTriggeredError
		if errors.As(err, &fbErr) {
			logger.Warn("LLM retry: consecutive 529 fallback triggered; surface upstream",
				zap.Int("consecutive_529", fbErr.Consecutive529),
			)
		}
	}
	if result == nil {
		result = &llmCallResult{}
	}

	// On clean success, replay the buffered text + tool_use events
	// onto `out` so downstream consumers (translator, sub-agent
	// driver post-processing) see them as if they were live-streamed.
	// On failure, emit nothing — the caller (subagent_driver /
	// queryloop) sees result.streamErr and emits its own
	// Error / MessageDelta(stop_reason=error) / MessageStop frames.
	//
	// Why replay instead of streaming live: see the doOnce comment.
	// Short version: a failed attempt-1 with partial chunks then
	// followed by a successful retry leaves the wire with stale
	// content the front-end can't reconcile. Buffer-then-replay
	// guarantees the wire only ever carries the FINAL successful
	// attempt's content.
	//
	// Ordering: text first (concatenated into one EngineEventText),
	// then tool_uses in arrival order. Interleaved text-tool-text-
	// tool isn't preserved, but that pattern is rare in current
	// tool-using LLMs (Anthropic / OpenAI almost always emit text
	// then tool calls; pure-text or pure-tool responses are also
	// common). Front-end rendering of "text bubble + tool card
	// sequence" is unaffected.
	if err == nil && out != nil && result != nil {
		if result.textBuf != "" {
			out <- types.EngineEvent{Type: types.EngineEventText, Text: result.textBuf}
		}
		for _, tc := range result.toolCalls {
			out <- types.EngineEvent{
				Type:      types.EngineEventToolUse,
				ToolUseID: tc.ID,
				ToolName:  tc.Name,
				ToolInput: tc.Input,
			}
		}
	}

	// After replay of buffered events, signal that the LLM stream is done
	// and tool execution is about to be dispatched. Translator turns this
	// into Phase=queued on each open tool card. Fires through planningOut
	// (not out) so retries — which discard the main stream's events —
	// still naturally retract these too if the next attempt fails before
	// we get here.
	if err == nil && planningOut != nil && result != nil {
		for _, tc := range result.toolCalls {
			select {
			case planningOut <- types.EngineEvent{
				Type:      types.EngineEventToolQueued,
				ToolUseID: tc.ID,
				ToolName:  tc.Name,
			}:
			default:
			}
		}
	}

	return result
}

// toAPIError converts a stream error into the typed *retry.APIError the
// Retryer expects. Already-classified errors (emitted by the bifrost
// adapter via classifyBifrostError) flow through unchanged. Anything
// unclassified is treated as a transient network error so we still
// retry on weird upstream errors — no worse than the old keyword
// matching, and Retryable=true is the right default for LLM transients.
func toAPIError(err error) *retry.APIError {
	if err == nil {
		return nil
	}
	var apiErr *retry.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return retry.ClassifyNetworkError(err)
}

// logSubmissionShape emits a one-line log of the call shape (msg/tool
// counts, prompt size). Logged BEFORE the retry loop so the line shows
// up exactly once per LLM round even when retries fire — distinct from
// the per-attempt "llm.call begin" inside the loop.
func logSubmissionShape(_ *zap.Logger, _ *provider.ChatRequest) {
	// Currently a no-op; the per-attempt "llm.call begin" already
	// carries the same fields. Kept as an extension hook so adding
	// once-per-round telemetry doesn't require touching the retry
	// loop.
}

// callLLMOnce performs one Chat call and fully consumes the stream,
// collecting text, tool calls, and usage. When out is non-nil, events
// are also emitted in real-time for streaming to the client (today
// callLLM always passes nil — see buffer-then-replay rationale). When
// planningOut is non-nil, the stream-aware tracker emits ToolPlanning
// / ToolPlanningProgress events live, even when out is nil — these are
// observation-only signals that may be retracted on retry.
//
// Timeouts (both optional, zero = disabled):
//   - timeouts.api       — total wall-clock cap on this attempt;
//                          enforced via context.WithTimeout
//   - timeouts.firstByte — silent-stall cap; a watchdog goroutine
//                          cancels the call (with errFirstByteTimeout
//                          as the cause) when no chunk lands within
//                          this budget. Disarms once the first chunk
//                          arrives, so legitimate long thinking
//                          preludes are not penalised.
//
// Both timeouts work in concert: api guards "stream that trickles
// forever", firstByte guards "stream that connects but never sends a
// byte". Without firstByte, the prior incident (10-min orphan_timeout
// on a step that never produced any event) would surface only as a
// watchdog kill, not as a typed retryable error.
func callLLMOnce(
	ctx context.Context,
	prov provider.Provider,
	req *provider.ChatRequest,
	out chan<- types.EngineEvent,
	planningOut chan<- types.EngineEvent,
	timeouts llmCallTimeouts,
	logger *zap.Logger,
) *llmCallResult {
	if timeouts.api > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeouts.api)
		defer cancel()
	}

	// Per-call cancel with cause so the first-byte watchdog can
	// distinguish itself from the API timeout / parent cancellation.
	callCtx, callCancel := context.WithCancelCause(ctx)
	defer callCancel(nil)

	// Watchdog: only armed when firstByte > 0. A buffered "first byte
	// arrived" channel lets the stream loop signal the watchdog
	// non-blockingly; sync.Once protects against close-twice from
	// concurrent first chunks (text + tool_use arriving back-to-back).
	firstByteCh := make(chan struct{})
	var firstByteOnce sync.Once
	signalFirstByte := func() {
		firstByteOnce.Do(func() { close(firstByteCh) })
	}
	if timeouts.firstByte > 0 {
		go func() {
			select {
			case <-firstByteCh:
				// First chunk arrived — disarm watchdog.
			case <-time.After(timeouts.firstByte):
				callCancel(errFirstByteTimeout)
			case <-callCtx.Done():
				// Call ended for unrelated reason (success / parent
				// cancel / api timeout). Disarm.
			}
		}()
	}

	callStart := time.Now()
	stream, err := prov.Chat(callCtx, req)
	if err != nil {
		return &llmCallResult{streamErr: classifyCtxErr(callCtx, err)}
	}

	result := &llmCallResult{}

	// toolPlanningTracker memoizes per-ToolUseID first-seen state and
	// throttle timestamps so we don't spam planningOut on every chunk.
	type planningState struct {
		nameSent bool
		lastEmit time.Time
	}
	tracker := map[string]*planningState{}
	const planningThresholdBytes = 200
	const planningThrottle = 50 * time.Millisecond

	// Stream-stuck watchdog: WARN every 30s if no new chunk has arrived.
	// Observability only — does NOT cancel; firstByteTimeout / apiTimeout
	// still own hard cancellation. Lets operators distinguish "vendor is
	// slow but alive" from "actually wedged" without flipping to DEBUG.
	streamWdDone := make(chan struct{})
	defer close(streamWdDone)
	var lastChunkMu sync.Mutex
	lastChunkAt := time.Now()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-streamWdDone:
				return
			case <-ticker.C:
				lastChunkMu.Lock()
				age := time.Since(lastChunkAt)
				lastChunkMu.Unlock()
				if age > 30*time.Second {
					// Informational heartbeat — upstream stalls aren't
					// a server-side defect on our side, just slowness
					// the retry budget will reap on its own. Keeping
					// at WARN inflated alerts; downgrade to INFO so
					// it still appears in default-level logs but
					// doesn't trip monitoring.
					logger.Info("llm.call.stream_stuck",
						zap.Duration("since_last_chunk", age),
					)
				}
			}
		}
	}()

	for evt := range stream.Events {
		switch evt.Type {
		case types.StreamEventText:
			if result.firstByteAt == 0 {
				result.firstByteAt = time.Since(callStart)
				signalFirstByte()
			}
			result.lastChunkAt = time.Since(callStart)
			lastChunkMu.Lock()
			lastChunkAt = time.Now()
			lastChunkMu.Unlock()
			result.textBuf += evt.Text
			if out != nil {
				out <- types.EngineEvent{Type: types.EngineEventText, Text: evt.Text}
			}
		case types.StreamEventToolUse:
			if evt.ToolCall != nil {
				if result.firstByteAt == 0 {
					result.firstByteAt = time.Since(callStart)
					signalFirstByte()
				}
				result.lastChunkAt = time.Since(callStart)
				lastChunkMu.Lock()
				lastChunkAt = time.Now()
				lastChunkMu.Unlock()
				result.toolCalls = append(result.toolCalls, *evt.ToolCall)
				if out != nil {
					out <- types.EngineEvent{
						Type:      types.EngineEventToolUse,
						ToolUseID: evt.ToolCall.ID,
						ToolName:  evt.ToolCall.Name,
						ToolInput: evt.ToolCall.Input,
					}
				}
				// ----- Phase A: stream-time planning observation -----
				// These events go to planningOut (NOT out / result.toolCalls), so
				// they reach the translator live while the main stream is still
				// buffered. Buffer-then-replay logic unchanged.
				if planningOut != nil && evt.ToolCall.Name != "" {
					id := evt.ToolCall.ID
					st, ok := tracker[id]
					if !ok {
						st = &planningState{}
						tracker[id] = st
					}
					if !st.nameSent {
						// T1 PLANNING — first time we see this tool_use ID with a name
						select {
						case planningOut <- types.EngineEvent{
							Type:      types.EngineEventToolPlanning,
							ToolUseID: id,
							ToolName:  evt.ToolCall.Name,
						}:
						default:
						}
						st.nameSent = true
					}
					// T2 PLANNING_ARGS — accumulated args ≥ threshold, throttled
					accumulated := len(evt.ToolCall.Input)
					if accumulated >= planningThresholdBytes && time.Since(st.lastEmit) >= planningThrottle {
						select {
						case planningOut <- types.EngineEvent{
							Type:      types.EngineEventToolPlanningProgress,
							ToolUseID: id,
							ToolName:  evt.ToolCall.Name,
							Bytes:     accumulated,
						}:
						default:
						}
						st.lastEmit = time.Now()
					}
				}
			}
		case types.StreamEventMessageEnd:
			result.endAt = time.Since(callStart)
			result.stopReason = evt.StopReason
			result.lastUsage = evt.Usage
			result.reasoning = evt.Reasoning
		case types.StreamEventError:
			// In-stream error; will be captured by stream.Err() below.
		}
	}

	if streamErr := stream.Err(); streamErr != nil {
		result.streamErr = classifyCtxErr(callCtx, streamErr)
	} else if cause := context.Cause(callCtx); cause != nil &&
		!errors.Is(cause, context.Canceled) && callCtx.Err() != nil {
		// Stream closed cleanly but ctx was cancelled (likely by our
		// first-byte watchdog or API timeout). Surface the cause so
		// retry classification + logs see the specific reason.
		result.streamErr = cause
	}

	return result
}

// classifyCtxErr surfaces the first-byte / API-timeout cause when the
// reported error is the generic ctx.Err(). Lets the retry layer treat
// "I cancelled this on purpose because the upstream stalled" as a
// retryable network-class error with a specific message, rather than
// a vague "context canceled".
func classifyCtxErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil &&
		!errors.Is(cause, context.Canceled) &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %s", cause, err.Error())
	}
	return err
}

// runLLMHeartbeat is the goroutine body that pings out every
// llmHeartbeatInterval until done closes (the LLM attempt finished) or
// ctx is cancelled. Each tick carries an UptimeMs derived from the
// supplied start anchor so the wire frame can show "this LLM call has
// been waiting for ~90s" — useful as a debugging crumb without
// allocating per-call goroutine state beyond the timer.
//
// Send on out is non-blocking: if the parent's buffer is full (slow
// consumer, dropped connection), we skip this tick rather than block
// the LLM retry path. Missing a single heartbeat doesn't risk
// orphan-timeout because Tracker.Touch resets the FULL OrphanTimeoutMs
// window each tick — 10× margin built into the interval choice.
func runLLMHeartbeat(
	ctx context.Context,
	done <-chan struct{},
	out chan<- types.EngineEvent,
	agentID string,
	start time.Time,
) {
	t := time.NewTicker(llmHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case now := <-t.C:
			select {
			case out <- types.EngineEvent{
				Type:    types.EngineEventLLMHeartbeat,
				AgentID: agentID,
				Duration: now.Sub(start).Milliseconds(),
			}:
			default:
				// Buffer full — drop silently. Watchdog still has
				// the previous tick's full window before firing.
			}
		}
	}
}

// startTimeForHeartbeat is a tiny indirection that exists only to let
// the heartbeat call site read like a one-liner — the closure-captured
// `time.Now()` would otherwise need to be lifted out so it can be
// referenced both at goroutine launch and (potentially) inside the
// loop's UptimeMs computation.
func startTimeForHeartbeat(t time.Time) time.Time { return t }
