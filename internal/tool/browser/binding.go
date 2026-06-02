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
	mu          sync.RWMutex
	sessionName string
	cdpEndpoint string
}

func NewTaskBinding(taskID string) *TaskBinding {
	return &TaskBinding{sessionName: BrowserTaskSessionName(taskID)}
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
	return b.sessionName
}

func (b *TaskBinding) CDPEndpoint() string {
	if b == nil {
		return ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cdpEndpoint
}

func (b *TaskBinding) UpdateCDPEndpoint(endpoint string) {
	if b == nil {
		return
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return
	}
	b.mu.Lock()
	b.cdpEndpoint = endpoint
	b.mu.Unlock()
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
		if result.IsError {
			continue
		}
		switch block.ToolName {
		case SessionCreateToolName, SessionStateToolName:
			if endpoint := extractCDPEndpoint(result); endpoint != "" {
				binding.UpdateCDPEndpoint(endpoint)
			}
		}
	}
}

func extractCDPEndpoint(result types.ToolResult) string {
	if result.Metadata != nil {
		if endpoint, _ := result.Metadata["cdp_endpoint"].(string); strings.TrimSpace(endpoint) != "" {
			return strings.TrimSpace(endpoint)
		}
		if tab, _ := result.Metadata["active_tab"].(map[string]any); tab != nil {
			if endpoint, _ := tab["cdp_endpoint"].(string); strings.TrimSpace(endpoint) != "" {
				return strings.TrimSpace(endpoint)
			}
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		return ""
	}
	if endpoint, _ := payload["cdp_endpoint"].(string); strings.TrimSpace(endpoint) != "" {
		return strings.TrimSpace(endpoint)
	}
	if tab, _ := payload["active_tab"].(map[string]any); tab != nil {
		if endpoint, _ := tab["cdp_endpoint"].(string); strings.TrimSpace(endpoint) != "" {
			return strings.TrimSpace(endpoint)
		}
	}
	return ""
}
