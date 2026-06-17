package browser

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"harnessclaw-go/pkg/types"
)

type bindingContextKey struct{}

type TaskBinding struct {
	mu                      sync.RWMutex
	taskID                  string
	browserSessionID        string
	activeTabID             string
	agentBrowserSessionName string
	cdpEndpoint             string
}

type BrowserSessionBinding struct {
	BrowserSessionID        string
	ActiveTabID             string
	AgentBrowserSessionName string
	CDPEndpoint             string
}

func NewTaskBinding(taskID string) *TaskBinding {
	return &TaskBinding{taskID: strings.TrimSpace(taskID)}
}

func WithTaskBinding(ctx context.Context, binding *TaskBinding) context.Context {
	return context.WithValue(ctx, bindingContextKey{}, binding)
}

func taskBindingFromContext(ctx context.Context) (*TaskBinding, bool) {
	b, ok := ctx.Value(bindingContextKey{}).(*TaskBinding)
	return b, ok && b != nil
}

func (b *TaskBinding) SessionName() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.agentBrowserSessionName
}

func (b *TaskBinding) BrowserSessionID() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.browserSessionID
}

func (b *TaskBinding) ActiveTabID() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.activeTabID
}

func (b *TaskBinding) CDPEndpoint() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cdpEndpoint
}

func (b *TaskBinding) IsReady() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.agentBrowserSessionName != "" && b.cdpEndpoint != "" && b.browserSessionID != ""
}

func (b *TaskBinding) UpdateBrowserSession(next BrowserSessionBinding) {
	if b == nil {
		return
	}
	sessionID := strings.TrimSpace(next.BrowserSessionID)
	sessionName := strings.TrimSpace(next.AgentBrowserSessionName)
	endpoint := strings.TrimSpace(next.CDPEndpoint)
	if sessionID == "" || sessionName == "" || endpoint == "" {
		return
	}
	activeTabID := strings.TrimSpace(next.ActiveTabID)
	b.mu.Lock()
	b.browserSessionID = sessionID
	b.activeTabID = activeTabID
	b.agentBrowserSessionName = sessionName
	b.cdpEndpoint = endpoint
	b.mu.Unlock()
}

func (b *TaskBinding) ClearIfBrowserSession(sessionID string) {
	if b == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.browserSessionID != sessionID {
		return
	}
	b.browserSessionID = ""
	b.activeTabID = ""
	b.agentBrowserSessionName = ""
	b.cdpEndpoint = ""
}

func UpdateTaskBindingFromResults(msg types.Message, results []types.ToolResult, binding *TaskBinding) {
	if binding == nil {
		return
	}
	resultIdx := 0
	for _, block := range msg.Content {
		if block.Type != types.ContentTypeToolUse {
			continue
		}
		if resultIdx >= len(results) {
			return
		}
		result := results[resultIdx]
		resultIdx++
		UpdateTaskBindingFromToolResult(block.ToolName, result, binding)
	}
}

func UpdateTaskBindingFromToolResult(toolName string, result types.ToolResult, binding *TaskBinding) {
	if binding == nil || result.IsError {
		return
	}
	switch toolName {
	case SessionCreateToolName, SessionStateToolName:
		if next, ok := extractBrowserSessionBinding(result.Metadata); ok {
			binding.UpdateBrowserSession(next)
		}
	case SessionCloseToolName:
		if sessionID := extractClosedBrowserSessionID(result); sessionID != "" {
			binding.ClearIfBrowserSession(sessionID)
		}
	}
}

func extractBrowserSessionBinding(metadata map[string]any) (BrowserSessionBinding, bool) {
	if metadata == nil {
		return BrowserSessionBinding{}, false
	}
	next := BrowserSessionBinding{
		BrowserSessionID:        stringFromMetadata(metadata, "session_id"),
		ActiveTabID:             stringFromMetadata(metadata, "active_tab_id"),
		AgentBrowserSessionName: stringFromMetadata(metadata, "agent_browser_session_name"),
		CDPEndpoint:             stringFromMetadata(metadata, "cdp_endpoint"),
	}
	if next.BrowserSessionID == "" || next.AgentBrowserSessionName == "" || next.CDPEndpoint == "" {
		return BrowserSessionBinding{}, false
	}
	return next, true
}

func extractClosedBrowserSessionID(result types.ToolResult) string {
	if result.Metadata != nil {
		if sessionID := stringFromMetadata(result.Metadata, "session_id"); sessionID != "" {
			return sessionID
		}
	}
	// Closing from public output is safe because it only uses the browser session id;
	// CDP endpoints and helper CLI sessions remain private metadata only.
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		return ""
	}
	return stringFromMetadata(payload, "session_id")
}

func stringFromMetadata(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
