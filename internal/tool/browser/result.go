package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/pkg/types"
)

type cliEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   json.RawMessage `json:"error"`
}

func runAgentBrowser(ctx context.Context, cfg config.BrowserAgentConfig, runner Runner, args []string) (*types.ToolResult, error) {
	execCtx, cancel := context.WithTimeout(ctx, cliTimeout(cfg))
	defer cancel()

	out, err := runner.Run(execCtx, args)
	if err != nil {
		content := strings.TrimSpace(string(out))
		if content == "" {
			content = err.Error()
		} else {
			content = content + ": " + err.Error()
		}
		return &types.ToolResult{
			Content:   "agent-browser command failed: " + content,
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
			Metadata:  map[string]any{"args": args},
		}, nil
	}

	result := parseCLIEnvelope(out)
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["args"] = args
	return result, nil
}

func parseCLIEnvelope(out []byte) *types.ToolResult {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return &types.ToolResult{Content: "", Metadata: map[string]any{}}
	}

	var env cliEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return &types.ToolResult{
			Content:  trimmed,
			Metadata: map[string]any{"raw_output": true},
		}
	}

	if !env.Success {
		msg := stringifyJSON(env.Error)
		if msg == "" {
			msg = "agent-browser returned success=false"
		}
		return &types.ToolResult{
			Content:   msg,
			IsError:   true,
			ErrorType: types.ToolErrorDependencyFail,
		}
	}

	return &types.ToolResult{
		Content: stringifyJSON(env.Data),
		Metadata: map[string]any{
			"agent_browser_success": true,
		},
	}
}

func stringifyJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, key := range []string{"snapshot", "text", "html", "url", "title", "message"} {
			if v, ok := obj[key]; ok {
				if out := stringifyJSON(v); out != "" {
					return out
				}
			}
		}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	formatted, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(formatted)
}
