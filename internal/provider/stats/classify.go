// Package stats provides a Provider decorator that records every LLM
// call's usage and context-window composition into the session metrics
// tracker. See docs/superpowers/specs/2026-05-12-session-metrics-design.md.
package stats

import (
	"encoding/json"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// classifyContext buckets the request's content into history /
// tool_results / system using a cheap char-based token estimate. The
// estimate is intentionally coarse: the dashboard reads the proportions,
// and the absolute "used" value is overwritten with the model's reported
// input_tokens on MessageEnd.
func classifyContext(req *provider.ChatRequest) (used, limit, history, toolResults, system int64) {
	// limit is the conversation-level context-window budget, NOT the
	// per-response cap (req.MaxTokens). Caller fills req.ContextWindow
	// from cfg.Agent.ContextWindow (already capped against the primary
	// endpoint's intrinsic limit upstream). 200_000 fallback covers
	// callers that don't set it — matches the engine compactor's
	// fallback so the dashboard and compactor agree on "the budget".
	limit = int64(req.ContextWindow)
	if limit <= 0 {
		limit = 200000
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			switch b.Type {
			case types.ContentTypeToolResult:
				toolResults += estimateTokens(b.ToolResult)
			case types.ContentTypeToolUse:
				history += estimateTokens(b.ToolInput)
			default:
				history += estimateTokens(b.Text)
			}
		}
	}
	// system bucket carries both the system_prompt string AND the tool
	// schemas (both occupy the same "system" slot from a token-budget
	// perspective). breakdownBuckets exposes them separately for the
	// observability log line.
	system = estimateTokens(req.System) + estimateToolSchemas(req.Tools)
	used = history + toolResults + system
	return
}

// estimateTokens uses the rough "4 chars ≈ 1 token" rule. Good enough
// for the dashboard's history vs tool_result vs system proportions.
func estimateTokens(s string) int64 {
	if s == "" {
		return 0
	}
	return int64(len(s)) / 4
}

// estimateToolSchemas serialises each schema and sums the estimates.
// The InputSchema map is small and JSON-marshallable; an error here is
// unlikely and silently dropped — the dashboard tolerates a 0 instead
// of erroring the request.
func estimateToolSchemas(tools []provider.ToolSchema) int64 {
	var total int64
	for _, t := range tools {
		total += estimateTokens(t.Name) + estimateTokens(t.Description)
		if t.InputSchema != nil {
			if b, err := json.Marshal(t.InputSchema); err == nil {
				total += int64(len(b)) / 4
			}
		}
	}
	return total
}
