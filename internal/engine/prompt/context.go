package prompt

import (
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tool"
)

// PromptContext carries all information a Section might need to
// render itself. It bridges existing engine types rather than
// duplicating them.
type PromptContext struct {
	// --- Session state ---
	SessionID string
	Turn      int  // current turn number (1-based)
	Compacted bool // true if auto-compact just ran

	// --- From existing engine types ---
	Session *session.Session // conversation history, metadata
	Tools   *tool.Registry   // registered tools

	// --- Prompt-specific config ---
	SystemPromptOverride string // if set, overrides the role section

	// --- Derived / computed ---
	TotalTokensUsed   int             // tokens consumed so far in conversation
	ContextWindowSize int             // model's context window in tokens
	PreviousSections  map[string]bool // sections rendered last turn

	// --- External data (populated by Builder before render) ---
	Memory       map[string]string // loaded memory entries
	EnvInfo      EnvSnapshot       // OS, CWD, shell
	Task         *TaskState        // current task state (nil = no active task)
	SkillListing string            // pre-formatted skill listing text (empty = no skills)
}

// TaskState tracks the agent's current execution state.
// Populated by the harness from session metadata or an external task system.
type TaskState struct {
	Goal           string   // the final objective
	Plan           []string // ordered steps to achieve the goal
	CurrentStep    int      // index into Plan (0-based)
	CompletedSteps []string // steps already finished (may differ from plan if re-planned)
	Blockers       []string // current obstacles preventing progress
}

// EnvSnapshot is a point-in-time capture of the runtime environment.
type EnvSnapshot struct {
	OS       string
	CWD      string
	Shell    string
	Platform string
	Date     string // current date in YYYY-MM-DD format
}
