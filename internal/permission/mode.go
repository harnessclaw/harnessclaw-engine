// Package permission implements the tool permission system.
//
// The original TypeScript codebase uses 7 permission modes, 8 rule sources,
// and an 11-reason decision system. This Go implementation preserves the
// core semantics while simplifying for the multi-channel service context.
package permission

// Mode controls how tool invocations are authorized.
type Mode string

const (
	ModeDefault     Mode = "default"      // read-only auto-allow, write requires confirmation
	ModePlan        Mode = "plan"         // read-only allowed, all writes require confirmation
	ModeAcceptEdits Mode = "accept_edits" // file edit tools auto-allowed, others need confirmation
	ModeBypass      Mode = "bypass"       // all tools allowed without confirmation
	ModeDontAsk     Mode = "dont_ask"     // allow all based on rules, never prompt
	ModeAuto        Mode = "auto"         // LLM classifier decides (future extension)
)

// ValidModes returns all valid permission modes.
func ValidModes() []Mode {
	return []Mode{ModeDefault, ModePlan, ModeAcceptEdits, ModeBypass, ModeDontAsk, ModeAuto}
}

// IsValid checks whether the mode is recognized.
func (m Mode) IsValid() bool {
	for _, v := range ValidModes() {
		if m == v {
			return true
		}
	}
	return false
}
