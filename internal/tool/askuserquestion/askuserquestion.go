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
				"description": "The clarification question presented to the user. Be specific and brief — one or two sentences. Examples: \"你说的王总是XX公司那个？\", \"周五下午要订哪种类型的会议室？\".",
				"minLength":   1,
			},
			"options": map[string]any{
				"type":        "array",
				"description": "Optional preset choices. The client renders them as selectable buttons or a dropdown. Omit to ask an open-ended question.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label": map[string]any{
							"type":        "string",
							"description": "Short label shown on the option (e.g. \"是的\", \"周五上午\").",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Optional longer explanation displayed alongside the label.",
						},
					},
					"required": []string{"label"},
				},
			},
			"multi": map[string]any{
				"type":        "boolean",
				"description": "When true and options are provided, the user may select multiple options. Default false.",
			},
			"allow_custom": map[string]any{
				"type":        "boolean",
				"description": "When true (default), the user may type free-text in addition to or instead of the preset options. Set false only for strictly-bounded yes/no style questions.",
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

const askDescription = `Pause and ask the user for clarification when their request is ambiguous, key information is missing, or several reasonable choices need a human decision.

Use AskUserQuestion when:
- The request is vague ("help me with the report" — which report? what aspect?)
- A named entity is ambiguous ("call 王总" — which 王总?)
- Multiple reasonable interpretations exist and picking the wrong one would waste effort
- A decision needs a value judgement that only the user can make

Do NOT use AskUserQuestion when:
- The answer is in the conversation history — re-read it
- A reasonable default exists — proceed and tell the user what you assumed
- The question can be answered by a quick search (use WebSearch / TavilySearch instead)

Input fields:
- question: the clarification you want from the user (required, one or two sentences)
- options: optional preset choices the user can pick from
- multi: optional bool, when true and options are provided the user may select multiple
- allow_custom: optional bool (default true) — when true the user may type a free-text answer beyond the preset options

The tool blocks until the user responds. Treat the response as the user's intent and continue with the task.`
