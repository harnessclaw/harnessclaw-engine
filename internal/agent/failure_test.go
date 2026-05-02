package agent

import (
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestIsTerminalError_HardErrors(t *testing.T) {
	cases := []struct {
		name string
		res  *SpawnResult
		want bool
	}{
		{"nil result", nil, false},
		{"nil terminal", &SpawnResult{}, false},
		{"completed", &SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalCompleted}}, false},
		{"model error", &SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalModelError}}, true},
		{"prompt too long", &SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalPromptTooLong}}, true},
		{"blocking limit", &SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalBlockingLimit}}, true},
		{
			"max turns without contract failures (soft)",
			&SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalMaxTurns}},
			false,
		},
		{
			"max turns with contract failures (hard)",
			&SpawnResult{
				Terminal:         &types.Terminal{Reason: types.TerminalMaxTurns},
				ContractFailures: []string{"missing role: report"},
			},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTerminalError(c.res); got != c.want {
				t.Errorf("IsTerminalError = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildFailureContent_SurfacesProviderError(t *testing.T) {
	// The bug this guards: when L2 hits a 502 on its first turn, result.Output
	// is "" because no text was generated. BuildFailureContent must lift
	// the Terminal.Message into the Content so emma knows what happened.
	res := &SpawnResult{
		Output: "", // empty — the failure happened pre-text
		Terminal: &types.Terminal{
			Reason:  types.TerminalModelError,
			Message: "bifrost: stream request failed: [status=502 provider=openai] provider API error: error code: 502",
			Turn:    1,
		},
	}
	got := BuildFailureContent(res, "Specialists")

	for _, must := range []string{
		"Specialists",                       // agent label
		"reason: model_error",               // structured field
		"502",                                // the actual provider error code
		"openai",                             // who failed
		"Do not fabricate",                   // emma-facing directive (top of msg)
		"do not invent content",              // closing directive
	} {
		if !strings.Contains(got, must) {
			t.Errorf("failure content missing %q\nfull text:\n%s", must, got)
		}
	}
}

func TestBuildFailureContent_IncludesContractFailures(t *testing.T) {
	// Distinct failure mode: the sub-agent finished but contract validation
	// rejected the submitted artifacts. Each rejection reason must reach
	// the parent so emma can decide "retry with different params" vs
	// "report blocker".
	res := &SpawnResult{
		Terminal: &types.Terminal{
			Reason:  types.TerminalMaxTurns,
			Message: "SubmitTaskResult rejected 4 times — abandoning task",
		},
		ContractFailures: []string{
			"artifacts[0]: artifact not found in store",
			"required output(s) not submitted: [comparison_table]",
		},
	}
	got := BuildFailureContent(res, "researcher")
	for _, must := range []string{
		"contract_failures:",
		"not found in store",
		"required output(s) not submitted",
		"comparison_table",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("failure content missing %q\nfull text:\n%s", must, got)
		}
	}
}

func TestBuildFailureContent_TruncatesPartialOutput(t *testing.T) {
	// When the L3 produced a long stream of text before failing, we don't
	// want to drown the structured fields under a wall of text — cap the
	// excerpt at 1000 bytes so emma's LLM still sees `reason:` / `detail:`
	// at the top of its tool_result.
	long := strings.Repeat("partial response ", 200) // ~3.4 KB
	res := &SpawnResult{
		Output:   long,
		Terminal: &types.Terminal{Reason: types.TerminalModelError, Message: "downstream blew up"},
	}
	got := BuildFailureContent(res, "x")
	if !strings.Contains(got, "...[truncated]") {
		t.Errorf("expected truncation marker; got:\n%s", got)
	}
}

func TestBuildFailureContent_NilSafe(t *testing.T) {
	// Defensive: tool plumbing should never crash when result is nil.
	got := BuildFailureContent(nil, "researcher")
	if !strings.Contains(got, "researcher") {
		t.Errorf("nil-result fallback should still mention agent label; got %q", got)
	}
}
