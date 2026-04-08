package types

import "encoding/json"

// ExecutionContext provides the full context for a tool execution within the query loop.
// This mirrors the TypeScript ToolUseContext — the engine populates it before each tool call.
type ExecutionContext struct {
	// SessionID is the active conversation session.
	SessionID string `json:"session_id"`

	// UserID is the authenticated user.
	UserID string `json:"user_id"`

	// ChannelName identifies the originating channel (feishu, websocket, http).
	ChannelName string `json:"channel_name"`

	// Messages is the current conversation history (read-only snapshot).
	Messages []Message `json:"messages"`

	// ToolCallID is the unique ID from the LLM's tool_use block.
	ToolCallID string `json:"tool_call_id"`

	// ToolName is the tool being invoked.
	ToolName string `json:"tool_name"`

	// ToolInput is the raw JSON input from the LLM.
	ToolInput json.RawMessage `json:"tool_input"`

	// AbortFunc can be called to signal an early abort of the query loop.
	AbortFunc func() `json:"-"`
}

// ---------------------------------------------------------------------------
// ToolUseContext — decomposed sub-context structure.
//
// The TypeScript codebase uses a single 40+ field ToolUseContext. Following the
// refactoring assessment (docs/31-tools-and-skills-deep-dive/13-refactoring-assessment.md),
// Go decomposes it into domain-specific sub-contexts for better cohesion.
// ---------------------------------------------------------------------------

// ToolUseContext is the full context passed to tool execution.
// It is decomposed into sub-contexts per the docs recommendation.
type ToolUseContext struct {
	Core       CoreContext
	File       FileContext
	Permission PermissionContext
	Agent      AgentContext
}

// CoreContext holds session-level and request-level identifiers.
type CoreContext struct {
	// SessionID is the active conversation session.
	SessionID string `json:"session_id"`
	// UserID is the authenticated user.
	UserID string `json:"user_id"`
	// ChannelName identifies the originating channel.
	ChannelName string `json:"channel_name"`
	// Messages is the current conversation history (read-only snapshot).
	Messages []Message `json:"messages,omitempty"`
	// ToolCallID is the unique ID from the LLM's tool_use block.
	ToolCallID string `json:"tool_call_id"`
	// ToolName is the tool being invoked.
	ToolName string `json:"tool_name"`
	// ToolInput is the raw JSON input from the LLM.
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	// AbortFunc can be called to signal an early abort of the query loop.
	AbortFunc func() `json:"-"`
}

// FileContext holds working directory and file operation settings.
type FileContext struct {
	// Cwd is the current working directory for file operations.
	Cwd string `json:"cwd"`
	// ReadFileTimeLimitMs is the max time for file reads (0 = no limit).
	ReadFileTimeLimitMs int `json:"read_file_time_limit_ms,omitempty"`
}

// PermissionRuleRef is a lightweight reference to a permission rule.
type PermissionRuleRef struct {
	ToolName    string `json:"tool_name"`
	RuleContent string `json:"rule_content,omitempty"` // optional argument pattern
	Source      string `json:"source"`
}

// PermissionContext holds permission mode and rules for the current scope.
type PermissionContext struct {
	// Mode is the active permission mode (default, plan, bypass, etc.).
	Mode string `json:"mode"`
	// AlwaysDenyRules are rules that always deny tool invocation.
	AlwaysDenyRules []PermissionRuleRef `json:"always_deny_rules,omitempty"`
	// AlwaysAllowRules are rules that always allow tool invocation.
	AlwaysAllowRules []PermissionRuleRef `json:"always_allow_rules,omitempty"`
	// AlwaysAskRules are rules that always require user confirmation.
	AlwaysAskRules []PermissionRuleRef `json:"always_ask_rules,omitempty"`
}

// AgentContext holds agent-specific fields when running inside a sub-agent.
type AgentContext struct {
	// AgentID is the unique identifier of this agent.
	AgentID string `json:"agent_id,omitempty"`
	// ParentAgentID is the ID of the agent that spawned this one.
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	// IsSubAgent is true when running inside a spawned sub-agent.
	IsSubAgent bool `json:"is_sub_agent,omitempty"`
	// AgentType classifies the agent: "sync", "async", "teammate", "coordinator".
	AgentType string `json:"agent_type,omitempty"`
}

// ToExecutionContext converts a ToolUseContext to the legacy ExecutionContext.
func (tuc *ToolUseContext) ToExecutionContext() *ExecutionContext {
	return &ExecutionContext{
		SessionID:   tuc.Core.SessionID,
		UserID:      tuc.Core.UserID,
		ChannelName: tuc.Core.ChannelName,
		Messages:    tuc.Core.Messages,
		ToolCallID:  tuc.Core.ToolCallID,
		ToolName:    tuc.Core.ToolName,
		ToolInput:   tuc.Core.ToolInput,
		AbortFunc:   tuc.Core.AbortFunc,
	}
}

// NewToolUseContextFromExecution creates a ToolUseContext from a legacy ExecutionContext.
func NewToolUseContextFromExecution(ec *ExecutionContext) *ToolUseContext {
	return &ToolUseContext{
		Core: CoreContext{
			SessionID:   ec.SessionID,
			UserID:      ec.UserID,
			ChannelName: ec.ChannelName,
			Messages:    ec.Messages,
			ToolCallID:  ec.ToolCallID,
			ToolName:    ec.ToolName,
			ToolInput:   ec.ToolInput,
			AbortFunc:   ec.AbortFunc,
		},
	}
}
