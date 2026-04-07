// Package errors defines domain error codes and sentinel errors.
package errors

import "fmt"

// Code is a machine-readable error code.
type Code string

const (
	CodeNotFound         Code = "NOT_FOUND"
	CodePermissionDenied Code = "PERMISSION_DENIED"
	CodeTimeout          Code = "TIMEOUT"
	CodeRateLimit        Code = "RATE_LIMIT"
	CodeInvalidInput     Code = "INVALID_INPUT"
	CodeProviderError    Code = "PROVIDER_ERROR"
	CodeToolExecError    Code = "TOOL_EXEC_ERROR"
	CodeSessionNotFound  Code = "SESSION_NOT_FOUND"
	CodeContextOverflow  Code = "CONTEXT_OVERFLOW"
	CodeInternal         Code = "INTERNAL"

	// API-layer error codes
	CodeAuthFailed        Code = "AUTH_FAILED"
	CodeCreditExhausted   Code = "CREDIT_EXHAUSTED"
	CodeOverloaded        Code = "OVERLOADED"
	CodeTokenRevoked      Code = "TOKEN_REVOKED"
	CodeModelUnavailable  Code = "MODEL_UNAVAILABLE"
	CodeFallbackTriggered Code = "FALLBACK_TRIGGERED"
)

// DomainError is the standard application error.
type DomainError struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *DomainError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error {
	return e.Cause
}

// New creates a new DomainError.
func New(code Code, msg string) *DomainError {
	return &DomainError{Code: code, Message: msg}
}

// Wrap creates a DomainError wrapping an underlying cause.
func Wrap(code Code, msg string, cause error) *DomainError {
	return &DomainError{Code: code, Message: msg, Cause: cause}
}

// Sentinel errors for use with errors.Is().
var (
	ErrSessionNotFound  = New(CodeSessionNotFound, "session not found")
	ErrAuthFailed       = New(CodeAuthFailed, "authentication failed")
	ErrOverloaded       = New(CodeOverloaded, "service overloaded")
	ErrCreditExhausted  = New(CodeCreditExhausted, "credit balance too low")
	ErrTokenRevoked     = New(CodeTokenRevoked, "token revoked")
	ErrModelUnavailable = New(CodeModelUnavailable, "model not available")
)
