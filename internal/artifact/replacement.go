package artifact

import "sync"

// ReplacementState tracks which tool_use_ids have had their tool_result
// content replaced with artifact references. Once a decision is made for a
// given tool_use_id (replace or keep), it is frozen for the lifetime of the
// session. This guarantees that the message prefix sent to the LLM never
// changes between turns, preserving prompt-cache hit rates.
//
// Design rationale: Anthropic's prompt cache keys off the message prefix.
// If an earlier tool_result toggles between full content and a reference
// across turns, the prefix changes and the cache is invalidated. By freezing
// the decision on first encounter we ensure prefix stability.
type ReplacementState struct {
	mu           sync.RWMutex
	seen         map[string]bool   // tool_use_id → true if seen (decision made)
	replacements map[string]string // tool_use_id → replacement text (only for replaced ones)
}

// NewReplacementState creates an empty replacement state.
func NewReplacementState() *ReplacementState {
	return &ReplacementState{
		seen:         make(map[string]bool),
		replacements: make(map[string]string),
	}
}

// Decide records the replacement decision for a tool_use_id. If the content
// should be replaced, pass the replacement text; otherwise pass "". Once a
// decision is recorded it cannot be changed.
//
// Returns true if the decision was newly recorded, false if it was already frozen.
func (rs *ReplacementState) Decide(toolUseID string, replacement string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if rs.seen[toolUseID] {
		return false // already frozen
	}
	rs.seen[toolUseID] = true
	if replacement != "" {
		rs.replacements[toolUseID] = replacement
	}
	return true
}

// IsReplaced returns (replacementText, true) if toolUseID was previously
// decided as replaced. Returns ("", false) if it was decided as kept or
// has not been seen yet.
func (rs *ReplacementState) IsReplaced(toolUseID string) (string, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	text, ok := rs.replacements[toolUseID]
	return text, ok
}

// IsSeen returns true if a decision has been made for the given tool_use_id.
func (rs *ReplacementState) IsSeen(toolUseID string) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.seen[toolUseID]
}

// ReplacedCount returns the number of tool_use_ids that were replaced.
func (rs *ReplacementState) ReplacedCount() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.replacements)
}

// SeenCount returns the total number of decisions made (both replaced and kept).
func (rs *ReplacementState) SeenCount() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.seen)
}
