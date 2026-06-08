package react_test

import (
	"testing"

	"harnessclaw-go/internal/engine/agent/scheduler/dispatch/react"
	"harnessclaw-go/internal/msgbus"
)

func TestShouldEscalateFromResult(t *testing.T) {
	tests := []struct {
		name   string
		res    msgbus.ResultMessage
		want   bool
	}{
		{
			name: "done status never escalates",
			res:  msgbus.ResultMessage{Status: "done"},
			want: false,
		},
		{
			name: "max_turns with free text escalates",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "max_turns: loop exhausted"},
			want: true,
		},
		{
			name: "model_error bare code escalates",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "model_error"},
			want: true,
		},
		{
			name: "blocking_limit escalates",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "blocking_limit"},
			want: true,
		},
		{
			name: "aborted_streaming does not escalate",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "aborted_streaming"},
			want: false,
		},
		{
			name: "cancelled status does not escalate",
			res:  msgbus.ResultMessage{Status: "cancelled", Reason: "aborted_tools"},
			want: false,
		},
		{
			name: "aborted_tools does not escalate",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "aborted_tools"},
			want: false,
		},
		{
			name: "prompt_too_long does not escalate",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "prompt_too_long"},
			want: false,
		},
		{
			name: "unknown reason defaults to false",
			res:  msgbus.ResultMessage{Status: "failed", Reason: "some_unknown_reason"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := react.ShouldEscalateFromResult(tc.res)
			if got != tc.want {
				t.Errorf("ShouldEscalateFromResult(%+v) = %v, want %v", tc.res, got, tc.want)
			}
		})
	}
}
