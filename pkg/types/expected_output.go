package types

import "encoding/json"

// ExpectedOutput is one entry in a task's deliverable contract. L2
// (Specialists, or any orchestrator) declares an []ExpectedOutput when
// dispatching to L3 so the framework can:
//
//	(1) inject the contract into L3's task prompt as a `<expected-outputs>`
//	    block (M1: tells the LLM what it must produce);
//	(2) enforce SubmitTaskResult schema with minItems ≥ count(required)
//	    (M3: schema-level "you must declare deliverables");
//	(3) validate each delivered artifact at submit time (M4: type/schema/
//	    size/role match a declared output);
//	(4) run static quality checks (M6: min_size_bytes / schema parseable).
//
// Producers should declare the smallest set of outputs that captures
// "what done looks like" — over-declaration creates rejection cascades
// when L3 produces equivalent-but-different shapes.
type ExpectedOutput struct {
	// Role is the contract-level identifier the L3 echoes back on
	// SubmitTaskResult. Required. Examples: "comparison_table",
	// "findings_report", "draft_email".
	Role string `json:"role"`

	// Type narrows what kind of artifact satisfies this output. One of
	// "structured" / "file" / "blob". Empty means any type is accepted.
	Type string `json:"type,omitempty"`

	// MIMEType further narrows the type ("text/csv", "application/json").
	// Optional; empty means any MIME accepted.
	MIMEType string `json:"mime_type,omitempty"`

	// Schema is the expected shape for structured outputs. Free-form
	// JSON so producers can specify column lists, field types, JSON
	// Schema fragments, etc. Used by M6 quality check, not by M4
	// existence check.
	Schema json.RawMessage `json:"schema,omitempty"`

	// MinSizeBytes guards against the "wrote a placeholder" failure mode
	// (doc §1 failure #3). Defaults to 1 when zero so empty content is
	// always rejected; producers needing a tighter bound set explicitly.
	MinSizeBytes int `json:"min_size_bytes,omitempty"`

	// Required marks must-deliver outputs. SubmitTaskResult schema sets
	// minItems = count(required) so the LLM cannot omit them. Optional
	// outputs (Required=false) may be skipped without rejection.
	Required bool `json:"required,omitempty"`

	// AcceptanceCriteria is a free-text description for the M6 semantic
	// quality check (LLM-as-judge). Not enforced by Milestone A; kept
	// here so the contract is forward-compatible with Milestone B.
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
}
