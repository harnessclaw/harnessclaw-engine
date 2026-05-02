package agent

import (
	"fmt"
	"strings"

	"harnessclaw-go/pkg/types"
)

// IsTerminalError reports whether the SpawnResult's terminal reason
// represents a hard failure that the calling tool should surface as
// IsError=true. Called by both the Task and Specialists tools so the
// "what counts as failure" rule lives in one place — divergence here
// would mean emma sees inconsistent error semantics depending on which
// dispatch tool the LLM picked.
func IsTerminalError(result *SpawnResult) bool {
	if result == nil || result.Terminal == nil {
		return false
	}
	switch result.Terminal.Reason {
	case types.TerminalModelError, types.TerminalPromptTooLong, types.TerminalBlockingLimit:
		return true
	}
	// Max-turns + nudge-cap exhaustion + submission-reject cap all flow
	// through TerminalMaxTurns. Treat as failure when the contract has
	// recorded specific failures (M3/M4 violations) — otherwise it's
	// "ran out of room", which the parent might want to interpret more
	// permissively.
	if result.Terminal.Reason == types.TerminalMaxTurns && len(result.ContractFailures) > 0 {
		return true
	}
	return false
}

// BuildFailureContent renders a clear, structured error report from a
// failed SpawnResult so the parent LLM gets actionable information
// rather than an empty Content with IsError=true.
//
// Without this, when a sub-agent's first LLM call hits a 502 the
// dispatching tool returns ToolResult{Content: result.Output, IsError: true}
// — but result.Output is "" because nothing got generated. emma's LLM
// sees an empty error result and either invents a recovery story or
// produces apologies that don't tell the user what actually happened.
//
// The format is intentionally machine-friendly so emma's LLM can parse
// the reason and decide between "report failure honestly" / "retry
// once" / "downgrade scope". Keeping fields named (`reason:`, `detail:`)
// rather than narrative makes it harder for the model to hallucinate
// around them.
func BuildFailureContent(result *SpawnResult, agentLabel string) string {
	if result == nil {
		return fmt.Sprintf("Sub-agent '%s' failed: no result returned.", agentLabel)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sub-agent '%s' did not complete successfully. Do not fabricate results.\n", agentLabel)
	if result.Terminal != nil {
		fmt.Fprintf(&b, "reason: %s\n", result.Terminal.Reason)
		if result.Terminal.Message != "" {
			fmt.Fprintf(&b, "detail: %s\n", result.Terminal.Message)
		}
		if result.Terminal.Turn > 0 {
			fmt.Fprintf(&b, "turn: %d\n", result.Terminal.Turn)
		}
	}
	if len(result.ContractFailures) > 0 {
		b.WriteString("contract_failures:\n")
		for _, f := range result.ContractFailures {
			b.WriteString("  - ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	if result.Output != "" {
		// Whatever text the sub-agent did emit before failing — kept
		// short so it doesn't drown the structured fields above.
		excerpt := result.Output
		if len(excerpt) > 1000 {
			excerpt = excerpt[:1000] + "...[truncated]"
		}
		b.WriteString("partial_output:\n")
		b.WriteString(excerpt)
		b.WriteString("\n")
	}
	// Closing directive — without it some models read the structured
	// fields and still narrate around them. This sentence is short on
	// purpose so it survives prompt compaction.
	b.WriteString("\nReport this failure to the user honestly — do not invent content.")
	return b.String()
}
