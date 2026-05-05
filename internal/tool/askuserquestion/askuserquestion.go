// Package askuserquestion implements the AskUserQuestion tool — emma's
// way to pause and ask the user for clarification when a request is
// ambiguous, when a key fact is missing, or when several reasonable
// options need a human decision.
//
// Execution model: AskUserQuestion is a *client-routed* tool. Under the
// production client_tools=true mode, the engine forwards the tool.call to
// the WebSocket client, which renders the question UI (preset options +
// optional free-text input) and returns the user's answer via the
// standard tool.result message. The server-side Execute() body is a
// safety net for client_tools=false (e.g., test harnesses that wire a
// real server-side dispatch); without a client to ask, the tool simply
// returns a clear error explaining the dependency.
package askuserquestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the registered name. The WebSocket client recognises this
// exact string when dispatching tool.call events to render the question UI.
const ToolName = "AskUserQuestion"

// askInput is the parsed payload from emma's LLM call.
type askInput struct {
	// Question is the prompt shown to the user. Required.
	Question string `json:"question"`

	// Options is an optional preset list. When non-empty the client may
	// render selectable choices in addition to (or instead of) free text.
	Options []Option `json:"options,omitempty"`

	// Multi indicates whether the user may pick more than one preset
	// option. Ignored when Options is empty.
	Multi bool `json:"multi,omitempty"`

	// AllowCustom controls whether the user may supply free-text in
	// addition to (or as a replacement for) the preset Options. Default
	// is true — emma's guidance is "options help, but the user may always
	// override with their own words".
	AllowCustom *bool `json:"allow_custom,omitempty"`
}

// Option is one preset choice the user can pick.
type Option struct {
	// Label is the short text shown on the option (required).
	Label string `json:"label"`

	// Description is an optional longer explanation rendered alongside.
	Description string `json:"description,omitempty"`
}

// Tool is the AskUserQuestion tool implementation.
type Tool struct {
	tool.BaseTool
	logger *zap.Logger
}

// New constructs an AskUserQuestion tool. logger may be nil.
func New(logger *zap.Logger) *Tool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Tool{logger: logger.Named("askuserquestion")}
}

func (t *Tool) Name() string             { return ToolName }
func (t *Tool) Description() string      { return askDescription }
func (t *Tool) IsReadOnly() bool         { return true }
func (t *Tool) IsConcurrencySafe() bool  { return false } // one question at a time
func (t *Tool) IsLongRunning() bool      { return true }  // blocks on the user

// CheckPermission auto-allows. Asking the user a question carries no risk
// (in fact it *reduces* risk by surfacing ambiguity before action).
func (t *Tool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

// InterruptBehavior cancels in-flight prompts on interrupt — the user has
// re-engaged so the pending question is moot.
func (t *Tool) InterruptBehavior() tool.InterruptMode {
	return tool.InterruptCancel
}

func (t *Tool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{
				"type":        "string",
				"description": "向用户澄清的问题。要具体、简短——一两句话。示例：\"你说的王总是XX公司那个？\"、\"周五下午要订哪种类型的会议室？\"。",
				"minLength":   1,
			},
			"options": map[string]any{
				"type":        "array",
				"description": "可选预设项。客户端会渲染成按钮或下拉框。完全开放式提问可省略本字段。",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label": map[string]any{
							"type":        "string",
							"description": "选项的短标签（例如 \"是的\"、\"周五上午\"）。",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "可选的更长说明，紧贴 label 一起展示。",
						},
					},
					"required": []string{"label"},
				},
			},
			"multi": map[string]any{
				"type":        "boolean",
				"description": "为 true 且提供 options 时，用户可多选。默认 false。",
			},
			"allow_custom": map[string]any{
				"type":        "boolean",
				"description": "为 true（默认）时用户可在预设外输入自由文本。仅在严格限定的是/否问题里设 false。",
			},
		},
		"required": []string{"question"},
	}
}

func (t *Tool) ValidateInput(raw json.RawMessage) error {
	var in askInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("invalid AskUserQuestion input: %w", err)
	}
	if strings.TrimSpace(in.Question) == "" {
		return fmt.Errorf("question is required and cannot be blank")
	}
	for i, opt := range in.Options {
		if strings.TrimSpace(opt.Label) == "" {
			return fmt.Errorf("options[%d].label is required", i)
		}
	}
	return nil
}

// IsClientRouted marks this tool as MUST-route-to-client. The engine's
// dispatch step (runQueryLoop / runSubAgentLoop) sees this and sends a
// tool.call wire message regardless of QueryEngineConfig.ClientTools —
// because there is no human on the server side to ask. See
// internal/tool.ClientRoutedTool for the contract.
func (t *Tool) IsClientRouted() bool { return true }

// Execute is the server-side fallback path. With IsClientRouted=true
// the engine should never reach this method. If it does, the dispatch
// wiring is broken and we surface a clear error so the LLM sees the
// failure instead of hanging.
func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (*types.ToolResult, error) {
	if err := t.ValidateInput(raw); err != nil {
		return &types.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	t.logger.Warn("AskUserQuestion.Execute reached server-side; this tool requires client_tools=true to function")
	return &types.ToolResult{
		Content: "AskUserQuestion is a client-routed tool — the running engine does not have a connected WebSocket client to deliver the question to. Configure client_tools=true.",
		IsError: true,
	}, nil
}

const askDescription = `当用户的请求有歧义、关键信息缺失、或几种合理选择需要人来定时，暂停并向用户提问。

何时使用 AskUserQuestion：
- 请求模糊（"帮我搞下那份报告" —— 哪份？哪部分？）
- 命名实体有歧义（"打给王总" —— 哪个王总？）
- 存在多种合理解读，选错会浪费工作量。
- 价值判断只有用户能做。

不要用 AskUserQuestion：
- 答案在对话历史里——回去重读。
- 有合理默认值——按默认走，并告诉用户你假设了什么。
- 一搜即得（改用 WebSearch / TavilySearch）。

输入字段：
- question：要向用户澄清的问题（必填，一两句话）。
- options：可选预设选项，用户可从中挑。
- multi：可选 bool，true 且提供 options 时允许多选。
- allow_custom：可选 bool（默认 true）——为 true 时用户可在预设外输入自由文本。

工具会阻塞直到用户回复。把回复视为用户意图，继续推进任务。`
