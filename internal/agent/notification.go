package agent

// WorkerNotification is the structured format for async worker completion
// results injected into a coordinator's conversation. This replaces plain
// text notifications with a machine-readable format.
type WorkerNotification struct {
	AgentID    string `json:"agent_id"`
	AgentName  string `json:"agent_name"`
	Status     string `json:"status"`      // completed / failed / killed
	Summary    string `json:"summary"`     // Worker's output summary
	Result     string `json:"result"`      // Full output (may be truncated)
	DurationMs int64  `json:"duration_ms"`
}
