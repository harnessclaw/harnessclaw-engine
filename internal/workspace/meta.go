package workspace

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// Meta is the L3-authored summary of one task's outputs. Written exactly
// once by MetaWrite to {taskDir}/meta.json.
type Meta struct {
	TaskID         string    `json:"task_id"`
	Agent          string    `json:"agent"`
	Status         Status    `json:"status"`
	Summary        string    `json:"summary"`
	Outputs        []Output  `json:"outputs,omitempty"`
	ConsumedInputs []string  `json:"consumed_inputs,omitempty"`
	EndedAt        time.Time `json:"ended_at,omitempty"`
}

// Output describes one file the L3 produced.
type Output struct {
	Path  string `json:"path"`
	Type  string `json:"type,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
}

// MaxSummaryRunes caps the meta summary length. The cap exists to keep
// summaries scannable (LLM-readable) and to push L3s toward describing
// shape rather than pasting content.
const MaxSummaryRunes = 500

// Validate enforces all MetaWrite-time invariants.
func (m *Meta) Validate() error {
	if m.TaskID == "" {
		return fmt.Errorf("meta: empty task_id")
	}
	switch m.Status {
	case StatusDone, StatusFailed:
	default:
		return fmt.Errorf("meta: status %q invalid (want done|failed; meta is only written at task terminal state)", m.Status)
	}
	if m.Summary == "" {
		return fmt.Errorf("meta: summary is required, non-empty")
	}
	if n := utf8.RuneCountInString(m.Summary); n > MaxSummaryRunes {
		return fmt.Errorf("meta: summary too long (%d runes, max %d) — keep it tight, do not paste content", n, MaxSummaryRunes)
	}
	return nil
}
