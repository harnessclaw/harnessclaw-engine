package types

import "time"

// SessionStats is the metrics snapshot returned by the session metrics
// HTTP endpoint and used as the wire shape persisted in
// sessions.metrics_json. See docs/superpowers/specs/2026-05-12-session-metrics-design.md.
type SessionStats struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`

	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	LatencyMsTotal int64 `json:"latency_ms_total"`
	LatencyMsAvg   int64 `json:"latency_ms_avg"`

	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	ThinkingTokens   int64   `json:"thinking_tokens"`
	ThinkingShare    float64 `json:"thinking_share"`

	ContextWindow ContextWindowStats `json:"context_window"`

	PerModel  []ModelStats    `json:"per_model"`
	SubAgents []SubAgentStats `json:"subagents"`

	LLMCalls  int `json:"llm_calls"`
	ToolCalls int `json:"tool_calls"`
}

// ContextWindowStats captures the composition of the most recent LLM
// call's input window — total used vs limit plus a coarse history /
// tool_results / system_prompt split. Used drives the "context window"
// panel in the metrics dashboard.
type ContextWindowStats struct {
	Used         int64 `json:"used"`
	Limit        int64 `json:"limit"`
	History      int64 `json:"history"`
	ToolResults  int64 `json:"tool_results"`
	SystemPrompt int64 `json:"system_prompt"`
}

// ModelStats is the per-model breakdown clients use to compute cost.
// Server-side cost is intentionally absent — pricing tables, discounts,
// and business logic live on the client.
type ModelStats struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ThinkingTokens   int64  `json:"thinking_tokens"`
	LLMCalls         int    `json:"llm_calls"`
}

// SubAgentStats is one row in the sub-agent breakdown table. Keyed by
// AgentRunID so multiple runs of the same agent type get distinct rows.
// Model is the single model the agent used; "mixed" if it spanned more
// than one (rare).
type SubAgentStats struct {
	AgentRunID string `json:"agent_run_id"`
	AgentID    string `json:"agent_id"`
	AgentType  string `json:"agent_type"` // sync | coordinator — runtime execution shape
	// SubagentType is the LLM-facing dispatch label (writer / researcher
	// / freelancer / etc.). Distinct from AgentType — leaf workers all
	// return "sync" for AgentType, which is useless for dashboard
	// disambiguation. Dashboards should prefer SubagentType when set.
	// Empty for legacy rows.
	SubagentType string `json:"subagent_type,omitempty"`
	Model        string `json:"model"`

	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ThinkingTokens   int64 `json:"thinking_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	LLMCalls   int    `json:"llm_calls"`
	DurationMs int64  `json:"duration_ms"`
	Status     string `json:"status"`
}
