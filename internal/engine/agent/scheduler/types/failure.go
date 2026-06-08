package types

// FailureReason classifies why a task failed; controls retry decisions.
type FailureReason string

const (
	FailLeaseExpired     FailureReason = "lease_expired"
	FailDeadlineExceeded FailureReason = "deadline_exceeded"
	FailBudgetExceeded   FailureReason = "budget_exceeded"
	FailPanicRecovered   FailureReason = "panic_recovered"
	FailWorkerError      FailureReason = "worker_error"
	FailCancelled        FailureReason = "cancelled"
	FailJudgeRejected    FailureReason = "judge_rejected"
)

const (
	// Transient failures — retryable when budget allows.
	FailureReasonTimeout    FailureReason = "timeout"
	FailureReasonRateLimit  FailureReason = "rate_limit"
	FailureReasonOverloaded FailureReason = "overloaded"
	FailureReasonNetwork    FailureReason = "network"
)

// Retryable reports whether onLifecycle.failed should attempt retry.
func (r FailureReason) Retryable() bool {
	switch r {
	case FailLeaseExpired, FailPanicRecovered, FailWorkerError:
		return true
	case FailureReasonTimeout, FailureReasonRateLimit, FailureReasonOverloaded, FailureReasonNetwork:
		return true
	}
	return false
}
