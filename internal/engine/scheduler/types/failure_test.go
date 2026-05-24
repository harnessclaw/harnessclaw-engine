package types_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestFailureRetryable(t *testing.T) {
	cases := []struct {
		r    types.FailureReason
		want bool
	}{
		{types.FailLeaseExpired, true},
		{types.FailPanicRecovered, true},
		{types.FailWorkerError, true},
		{types.FailDeadlineExceeded, false},
		{types.FailBudgetExceeded, false},
		{types.FailCancelled, false},
		{types.FailJudgeRejected, false},
	}
	for _, c := range cases {
		if got := c.r.Retryable(); got != c.want {
			t.Errorf("%s.Retryable() = %v, want %v", c.r, got, c.want)
		}
	}
}
