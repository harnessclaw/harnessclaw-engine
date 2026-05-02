package agent

import "harnessclaw-go/pkg/types"

// SpawnResult holds the output of a completed sub-agent execution.
type SpawnResult struct {
	// Output is the concatenation of all assistant text blocks from the sub-agent.
	// This is the full output; emma receives only Summary via tool_result.
	Output string

	// Summary is the <summary> tag content extracted from Output.
	// If the sub-agent didn't include a <summary> tag, this is the first
	// non-empty paragraph of Output (≤ 200 chars).
	// This is what gets returned to emma in the tool_result.
	Summary string

	// Status indicates the outcome: "completed", "max_turns", "error", "aborted".
	Status string

	// Attempts is how many times SpawnSync tried (1 = first attempt succeeded).
	Attempts int

	// Terminal describes why the sub-agent's query loop ended.
	Terminal *types.Terminal

	// Usage is the cumulative token consumption across all turns.
	Usage *types.Usage

	// SessionID is the sub-agent's ephemeral session identifier (for debugging).
	SessionID string

	// AgentID is the unique identifier for this sub-agent invocation.
	AgentID string

	// Deliverables lists files produced by the sub-agent via FileWrite.
	// Detected automatically from tool_end events with render_hint "file_info".
	Deliverables []types.Deliverable

	// DeniedTools lists tool names that the sub-agent attempted but were denied
	// by the permission checker. Reported back so the parent can act on them.
	DeniedTools []string

	// NumTurns is the number of query loop iterations completed.
	NumTurns int

	// SubmittedArtifacts is the list of refs the L3 declared via the
	// SubmitTaskResult tool. Populated on tasks that had ExpectedOutputs;
	// nil otherwise. The parent (L2) reads this to integrate without
	// re-scanning the per-tool stream — it's the canonical "deliverables"
	// set, distinct from the looser "everything this agent wrote" view.
	SubmittedArtifacts []types.ArtifactRef

	// ContractFailures records per-validation-failure reasons when the
	// L3 hit the retry cap without a passing SubmitTaskResult. Empty on
	// success. Used by the parent to decide whether to retry, downgrade,
	// or escalate.
	ContractFailures []string
}
