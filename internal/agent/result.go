package agent

import "harnessclaw-go/pkg/types"

// SpawnResult holds the output of a completed sub-agent execution.
type SpawnResult struct {
	// Output is the concatenation of all assistant text blocks from the sub-agent.
	Output string

	// Terminal describes why the sub-agent's query loop ended.
	Terminal *types.Terminal

	// Usage is the cumulative token consumption across all turns.
	Usage *types.Usage

	// SessionID is the sub-agent's ephemeral session identifier (for debugging).
	SessionID string

	// AgentID is the unique identifier for this sub-agent invocation.
	AgentID string

	// DeniedTools lists tool names that the sub-agent attempted but were denied
	// by the permission checker. Reported back so the parent can act on them.
	DeniedTools []string

	// NumTurns is the number of query loop iterations completed.
	NumTurns int
}
