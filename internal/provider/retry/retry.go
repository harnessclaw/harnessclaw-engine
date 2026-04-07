// Package retry provides a retry engine for LLM API calls with exponential
// backoff, jitter, 529-overload fallback detection, and error classification.
//
// This is modeled after the original TypeScript withRetry() implementation
// which handles: normal retries (10 attempts, 500ms*2^n backoff, 32s cap,
// 25% jitter), consecutive 529 fallback triggering, and non-retryable error
// short-circuiting.
package retry

import (
	"context"
	"math"
	"math/rand"
	"time"

	"go.uber.org/zap"
)

// Config holds retry behavior parameters.
type Config struct {
	// MaxRetries is the maximum number of retry attempts (default 10).
	MaxRetries int
	// InitialDelay is the base delay before the first retry (default 500ms).
	InitialDelay time.Duration
	// MaxDelay is the upper bound on backoff delay (default 32s).
	MaxDelay time.Duration
	// JitterFraction controls the random jitter range as a fraction of delay (default 0.25).
	JitterFraction float64
	// FallbackAfter529 is the number of consecutive 529 errors before triggering
	// a fallback model switch (default 3).
	FallbackAfter529 int
}

// DefaultConfig returns the default retry configuration matching the original
// TypeScript implementation's normal mode.
func DefaultConfig() *Config {
	return &Config{
		MaxRetries:       10,
		InitialDelay:     500 * time.Millisecond,
		MaxDelay:         32 * time.Second,
		JitterFraction:   0.25,
		FallbackAfter529: 3,
	}
}

// APIErrorType classifies the kind of API error encountered.
type APIErrorType string

const (
	ErrPromptTooLong   APIErrorType = "prompt_too_long"
	ErrMaxOutputTokens APIErrorType = "max_output_tokens"
	ErrRateLimit       APIErrorType = "rate_limit"   // HTTP 429
	ErrOverloaded      APIErrorType = "overloaded"    // HTTP 529
	ErrAuthFailed      APIErrorType = "auth_failed"   // HTTP 401
	ErrTokenRevoked    APIErrorType = "token_revoked"  // HTTP 403
	ErrServerError     APIErrorType = "server_error"   // HTTP 5xx (excluding 529)
	ErrNetworkError    APIErrorType = "network_error"  // ECONNRESET, EPIPE, etc.
	ErrUnknown         APIErrorType = "unknown"
)

// APIError is a classified API error carrying the HTTP status, message,
// error type, retryability, and the original error.
type APIError struct {
	StatusCode int
	Message    string
	Type       APIErrorType
	Retryable  bool
	Raw        error
}

func (e *APIError) Error() string { return e.Message }

func (e *APIError) Unwrap() error { return e.Raw }

// FallbackTriggeredError signals that too many consecutive 529 errors have
// occurred and the caller should switch to a fallback model.
type FallbackTriggeredError struct {
	Consecutive529 int
	Message        string
}

func (e *FallbackTriggeredError) Error() string { return e.Message }

// Retryer executes operations with automatic retry, exponential backoff,
// jitter, and 529-based fallback detection.
type Retryer struct {
	config         *Config
	consecutive529 int
	logger         *zap.Logger
}

// New creates a Retryer. If cfg is nil, DefaultConfig() is used.
func New(cfg *Config, logger *zap.Logger) *Retryer {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Retryer{config: cfg, logger: logger}
}

// Do executes fn with retry logic. fn should return an *APIError (or a type
// that wraps one) for retryable API failures. Non-API errors are returned
// immediately without retry.
//
// Returns nil on success, *FallbackTriggeredError when consecutive 529s exceed
// the threshold, or the last error on exhaustion / non-retryable failure.
func (r *Retryer) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		err := fn(ctx)
		if err == nil {
			r.consecutive529 = 0
			return nil
		}

		apiErr, ok := err.(*APIError)
		if !ok {
			// Non-API error: do not retry.
			return err
		}
		lastErr = apiErr

		// Track consecutive 529 overloaded errors.
		if apiErr.Type == ErrOverloaded {
			r.consecutive529++
			if r.consecutive529 >= r.config.FallbackAfter529 {
				return &FallbackTriggeredError{
					Consecutive529: r.consecutive529,
					Message:        "consecutive 529 errors triggered fallback",
				}
			}
		} else {
			r.consecutive529 = 0
		}

		// Non-retryable errors are returned immediately.
		if !apiErr.Retryable {
			return apiErr
		}

		// Last attempt exhausted: no more retries.
		if attempt == r.config.MaxRetries {
			break
		}

		// Compute backoff delay with jitter.
		delay := r.calculateDelay(attempt)
		r.logger.Warn("retrying API call",
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", r.config.MaxRetries),
			zap.Duration("delay", delay),
			zap.String("error_type", string(apiErr.Type)),
			zap.Int("status_code", apiErr.StatusCode),
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

// Reset clears the consecutive 529 counter. Call this when a fallback model
// is activated and the retryer should start fresh.
func (r *Retryer) Reset() {
	r.consecutive529 = 0
}

// calculateDelay computes the backoff duration for the given attempt index
// using exponential backoff with jitter: delay = InitialDelay * 2^attempt,
// capped at MaxDelay, with random jitter in [-JitterFraction, +JitterFraction].
func (r *Retryer) calculateDelay(attempt int) time.Duration {
	delay := float64(r.config.InitialDelay) * math.Pow(2, float64(attempt))
	if delay > float64(r.config.MaxDelay) {
		delay = float64(r.config.MaxDelay)
	}
	// Apply jitter: uniform in [-fraction, +fraction] of computed delay.
	jitter := delay * r.config.JitterFraction * (rand.Float64()*2 - 1)
	delay += jitter
	if delay < 0 {
		delay = float64(r.config.InitialDelay)
	}
	return time.Duration(delay)
}

// ClassifyHTTPError maps an HTTP status code and response body to a classified
// *APIError with appropriate retryability.
func ClassifyHTTPError(statusCode int, body string, raw error) *APIError {
	switch {
	case statusCode == 401:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrAuthFailed,
			Retryable:  false,
			Raw:        raw,
		}
	case statusCode == 403:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrTokenRevoked,
			Retryable:  false,
			Raw:        raw,
		}
	case statusCode == 413:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrPromptTooLong,
			Retryable:  false,
			Raw:        raw,
		}
	case statusCode == 429:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrRateLimit,
			Retryable:  true,
			Raw:        raw,
		}
	case statusCode == 529:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrOverloaded,
			Retryable:  true,
			Raw:        raw,
		}
	case statusCode >= 500:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrServerError,
			Retryable:  true,
			Raw:        raw,
		}
	default:
		return &APIError{
			StatusCode: statusCode,
			Message:    body,
			Type:       ErrUnknown,
			Retryable:  false,
			Raw:        raw,
		}
	}
}

// ClassifyNetworkError wraps a network-level error (ECONNRESET, timeout, etc.)
// as a retryable APIError.
func ClassifyNetworkError(err error) *APIError {
	return &APIError{
		StatusCode: 0,
		Message:    err.Error(),
		Type:       ErrNetworkError,
		Retryable:  true,
		Raw:        err,
	}
}
