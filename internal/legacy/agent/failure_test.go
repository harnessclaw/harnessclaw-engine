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
	got := BuildFailureContent(res, "scheduler")

	for _, must := range []string{
		"scheduler",                       // agent label
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

// TestBuildFailureContent_ListsResidualFilesForRecovery pins the
// recovery-from-failure surface: when a sub-agent fails after writing
// real files to disk (e.g. a half-finished docx-generator script), the
// failure message must name those files so the parent LLM can resume
// from them instead of dispatching a fresh sub-agent that redoes the
// work from zero. Observed pre-fix: L3 #1 failed after writing a 4.5KB
// generate_docx.js; L2 silently re-dispatched and L3 #2 burned 40
// turns rewriting the exact same script without ever reading the
// surviving file.
func TestBuildFailureContent_ListsResidualFilesForRecovery(t *testing.T) {
	res := &SpawnResult{
		Terminal: &types.Terminal{Reason: types.TerminalModelError, Message: "499 client_disconnected", Turn: 17},
		ResidualFiles: []ResidualFile{
			{Path: "/sessions/x/tasks/t1/generate_docx.js", SizeBytes: 4564},
			{Path: "/sessions/x/tasks/t1/notes.md", SizeBytes: 312},
		},
	}
	got := BuildFailureContent(res, "freelancer")

	for _, must := range []string{
		"produced_files",
		"generate_docx.js",
		"4564 bytes",
		"notes.md",
		"312 bytes",
		"read and resume", // steering language — without it the LLM tends to ignore the list
	} {
		if !strings.Contains(got, must) {
			t.Errorf("failure content missing %q\nfull text:\n%s", must, got)
		}
	}
}

// TestBuildFailureContent_TruncatesLongResidualLists keeps the failure
// summary from blowing up when a scratch dir accumulates many files.
// Without the cap, a long-running sub-agent that wrote 200 intermediate
// files would shove 200 lines into the parent's tool_result and crowd
// out the actually useful fields above (reason, contract_failures).
func TestBuildFailureContent_TruncatesLongResidualLists(t *testing.T) {
	files := make([]ResidualFile, 25)
	for i := range files {
		files[i] = ResidualFile{Path: "/x/f", SizeBytes: 1}
	}
	res := &SpawnResult{
		Terminal:      &types.Terminal{Reason: types.TerminalModelError},
		ResidualFiles: files,
	}
	got := BuildFailureContent(res, "freelancer")
	if !strings.Contains(got, "... and 5 more") {
		t.Errorf("expected truncation marker '... and 5 more'; got:\n%s", got)
	}
	if strings.Count(got, "/x/f") > 20 {
		t.Errorf("listed more than 20 paths; truncation cap broken")
	}
}

// TestBuildFailureContent_NoResidualSectionWhenEmpty keeps the noise
// floor low: a sub-agent that failed before writing anything must NOT
// emit an empty "produced_files:" header — that would just confuse the
// LLM.
func TestBuildFailureContent_NoResidualSectionWhenEmpty(t *testing.T) {
	res := &SpawnResult{
		Terminal: &types.Terminal{Reason: types.TerminalModelError},
	}
	got := BuildFailureContent(res, "freelancer")
	if strings.Contains(got, "produced_files") {
		t.Errorf("must not render produced_files section when nothing was written; got:\n%s", got)
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
