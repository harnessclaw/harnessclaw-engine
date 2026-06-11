package permission

// Decision is the tri-state outcome of a permission check.
type Decision string

const (
	Allow Decision = "allow" // tool may execute
	Deny  Decision = "deny"  // tool must not execute
	Ask   Decision = "ask"   // channel should prompt the user for confirmation
)

// Reason explains why a particular decision was made.
type Reason string

const (
	ReasonRule     Reason = "rule"      // matched an explicit rule
	ReasonMode     Reason = "mode"      // derived from the active permission mode
	ReasonReadOnly Reason = "read_only" // tool is read-only, auto-allowed
	ReasonBypass   Reason = "bypass"    // bypass mode active
	ReasonHook     Reason = "hook"      // decided by a permission hook
	ReasonDefault  Reason = "default"   // no rule matched, fell through to default
)

// Result carries a permission decision together with its rationale.
type Result struct {
	Decision Decision `json:"decision"`
	Reason   Reason   `json:"reason"`
	Message  string   `json:"message,omitempty"` // human-readable explanation
}
