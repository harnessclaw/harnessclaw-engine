package react_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/scheduler/dispatch"
	"harnessclaw-go/internal/engine/agent/scheduler/dispatch/react"
)

func TestEscalateHookNilIsNoOp(t *testing.T) {
	strat := react.New(dispatch.Capabilities{})
	_ = strat // compile check — nil hook = no crash
}

func TestEscalationRequestedError(t *testing.T) {
	err := &dispatch.EscalationRequestedError{TaskID: "t-1"}
	if err.Error() == "" {
		t.Fatal("expected non-empty error string")
	}
}
