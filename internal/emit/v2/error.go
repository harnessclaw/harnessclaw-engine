package emitv2

// ErrorType is the controlled enum of error categories. Single source of
// truth — replaces v1's three parallel error structs (ErrorDetail, ErrorBody,
// FailurePayload) and the ad-hoc TaskDispatch.ErrorType strings.
//
// Producers MUST pick the closest matching value; clients MUST treat
// unknown values as ErrorTypeInternal and fall back gracefully.
type ErrorType string

const (
	// Tool / execution
	ErrorTypeToolTimeout      ErrorType = "tool_timeout"
	ErrorTypeRateLimit        ErrorType = "rate_limit"
	ErrorTypeOverloaded       ErrorType = "overloaded"
	ErrorTypeContractFail     ErrorType = "contract_fail"
	ErrorTypeDependencyFail   ErrorType = "dependency_fail"
	ErrorTypePermissionDenied ErrorType = "permission_denied"
	ErrorTypeInvalidInput     ErrorType = "invalid_input"

	// User / session
	ErrorTypeUserAborted ErrorType = "user_aborted"

	// Agent / orchestration
	ErrorTypeMaxTurns        ErrorType = "max_turns"
	ErrorTypeContextExceeded ErrorType = "context_exceeded"
	ErrorTypeOrphanTimeout   ErrorType = "orphan_timeout"
	ErrorTypeBudgetExhausted ErrorType = "budget_exhausted"

	// LLM
	ErrorTypeModelError ErrorType = "model_error"

	// Catch-all
	ErrorTypeInternal ErrorType = "internal"
)

// ErrorInfo is the canonical error block. Carried only on
// card.close{status:failed}.payload.error or session.event{kind:error}.payload.error.
//
// UserMessage is the persona-friendly fallback L1 quotes back to the user
// (so the user never sees raw stack traces or internal codes). The
// errorTypeMeta registry provides a default UserMessage when callers
// don't supply one.
type ErrorInfo struct {
	Type         ErrorType `json:"type"`
	Code         string    `json:"code,omitempty"`
	Message      string    `json:"message"`
	UserMessage  string    `json:"user_message,omitempty"`
	Retryable    bool      `json:"retryable,omitempty"`
	RetryAfterMs int       `json:"retry_after_ms,omitempty"`
	Recovery     *Recovery `json:"recovery,omitempty"`
}

// Recovery describes what the framework decided to do about a failure.
// The renderer can show the action ("retry", "fallback") on the failure
// card so the user understands the system isn't silently dropping work.
type Recovery struct {
	Action     string `json:"action"`               // retry | fallback | abort
	NextCardID string `json:"next_card_id,omitempty"` // pointer to the replacement card
}

// NewError constructs an ErrorInfo with sensible defaults derived from
// the ErrorType (see registry.go errorTypeMeta). Caller can override any
// field via the With* helpers.
func NewError(typ ErrorType, message string) *ErrorInfo {
	e := &ErrorInfo{Type: typ, Message: message}
	if meta, ok := errorTypeMeta[typ]; ok {
		e.UserMessage = meta.DefaultUserMessage
		e.Retryable = meta.DefaultRetryable
	}
	return e
}

// WithUserMessage overrides the default user-facing fallback.
func (e *ErrorInfo) WithUserMessage(msg string) *ErrorInfo {
	e.UserMessage = msg
	return e
}

// WithCode adds a free-form machine-readable subcode.
func (e *ErrorInfo) WithCode(code string) *ErrorInfo {
	e.Code = code
	return e
}

// WithRetryable overrides the registry default.
func (e *ErrorInfo) WithRetryable(retryable bool) *ErrorInfo {
	e.Retryable = retryable
	return e
}

// WithRetryAfter signals the client to back off N ms before retrying.
func (e *ErrorInfo) WithRetryAfter(ms int) *ErrorInfo {
	e.RetryAfterMs = ms
	return e
}

// WithRecovery attaches a Recovery action.
func (e *ErrorInfo) WithRecovery(action, nextCardID string) *ErrorInfo {
	e.Recovery = &Recovery{Action: action, NextCardID: nextCardID}
	return e
}
