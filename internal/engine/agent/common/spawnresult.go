package common

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

	// NeedsPlanning is set true when the L3 sub-agent called
	// EscalateToPlanner instead of SubmitTaskResult — i.e., it judged the
	// task undoable as scoped and asked the parent to re-plan. The L2
	// caller should NOT treat this as a hard failure; it's a structured
	// hand-back. Only sub-agent runs (TierSubAgent) can produce this.
	NeedsPlanning bool

	// EscalationReason is the L3's explanation of why escalation was
	// necessary. Populated only when NeedsPlanning is true. Surfaced to
	// the planner so it can decide what to do (retry with wider scope,
	// pick a different agent, ask the user, abort).
	EscalationReason string

	// SuggestedNextSteps is the L3's hint about how to recover. Optional;
	// the planner is free to ignore it. Populated only when NeedsPlanning
	// is true.
	SuggestedNextSteps string

	// SelfCheckFailures records per-failure reasons when the sub-agent's
	// post-submission self-check rejected the output even after
	// SubmitTaskResult passed schema validation. The parent loop reads
	// this to decide whether the L3 needs another iteration. Empty when
	// self-check passed or no self-check was configured.
	SelfCheckFailures []string

	// CoordinatorMode is the L2 mode that ran this spawn ("react" /
	// "plan" / future). Empty for L3 / non-coordinator spawns. Surfaced
	// to telemetry + ToolResult metadata so operators can correlate
	// "this task ran in plan mode" with budget / latency data.
	CoordinatorMode string

	// EscalatedFromMode, when non-empty, means a coordinator promoted
	// this run mid-flight (e.g. ReAct → Plan via D-mode escalation).
	// CoordinatorMode reports the FINAL mode that produced the output;
	// EscalatedFromMode tells you where it started.
	EscalatedFromMode string

	// BudgetSpent reports the cumulative consumption tracked by the L2
	// BudgetTracker. Empty on L3 spawns or coordinator-tier spawns that
	// never started a tracker. Surfaced so emma can explain "降级原因：
	// token 预算耗尽 (used 250k of 200k)" without inferring from logs.
	BudgetSpent BudgetSpent

	// ResidualFiles is the directory listing of the spawn's task_dir at
	// exit time — every file the sub-agent left on disk, whether or not
	// it was promoted to a deliverable or submitted via the contract.
	// Populated by the module on completion (success or failure).
	//
	// The point is recovery: when a sub-agent fails mid-task (e.g.
	// upstream 499 / model_error after 17 turns of work), the L2 parent
	// gets a tool_result that names exactly which files survived. Before
	// this, L2 would see only the failure reason and dispatch a fresh
	// sub-agent that redid the same work from scratch, ignoring the
	// generate_docx.js sitting on disk from the previous attempt. With
	// ResidualFiles surfaced via BuildFailureContent, the parent LLM
	// can decide to read the file and resume instead of restarting.
	ResidualFiles []ResidualFile
}

// ResidualFile is a single entry in SpawnResult.ResidualFiles. Kept
// minimal on purpose: the parent LLM needs the path to read it and the
// size to decide whether to bother — anything richer (modtime, hash,
// content snippet) belongs on the caller's next `read` call, not in
// the failure summary.
type ResidualFile struct {
	// Path is the absolute path on disk, ready to be passed back as a
	// `read({file_path: ...})` argument by the next sub-agent.
	Path string `json:"path"`
	// SizeBytes is the file size at scan time.
	SizeBytes int64 `json:"size_bytes"`
}

// BudgetSpent is the wire-shape mirror of engine.BudgetSnapshot. Lives
// here (not in engine) to avoid a cycle: agent.SpawnResult is consumed
// by tools that can't import engine.
type BudgetSpent struct {
	TokensUsed  int    `json:"tokens_used,omitempty"`
	LLMCalls    int    `json:"llm_calls,omitempty"`
	Failures    int    `json:"failures,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms,omitempty"`
	Exceeded    bool   `json:"exceeded,omitempty"`
	ExceededWhy string `json:"exceeded_why,omitempty"`
}
