package emitv2

import "time"

// Registry: single source of truth for cardKind metadata and errorType
// metadata. Drives default Hint templates, severity, lifecycle tracking,
// orphan timeouts, and ErrorInfo defaults.
//
// Adding a new card_kind: add a row to cardMeta. The Builder, Lifecycle
// Watchdog, and (future) doc generator all read from here.

// CardMeta describes per-card-kind defaults and behaviour.
type CardMeta struct {
	// DefaultIcon is rendered on the card when Hint.Icon is empty.
	DefaultIcon string
	// DefaultRole is the AgentRole on the envelope when caller doesn't
	// override (typical: tool/agent => worker, plan/step => orchestrator,
	// turn/message => persona).
	DefaultRole AgentRole
	// TitleTpl is a tiny template for default Hint.Title. Tokens are
	// substituted from the payload at emit time. Supported tokens:
	//   {tool_name}, {agent_name}, {subagent_type}, {step_id}, {plan_id},
	//   {turn_no}, {artifact_name}.
	TitleTpl string
	// Lifecycle controls whether the orphan watchdog tracks this kind.
	// "tracked" cards must call card.close before OrphanTimeoutMs elapses
	// or the watchdog emits a synthetic failed close.
	Lifecycle Lifecycle
	// OrphanTimeoutMs is the watchdog deadline. Ignored when Lifecycle is
	// "untracked".
	OrphanTimeoutMs int64
}

// Lifecycle is the watchdog policy.
type Lifecycle string

const (
	LifecycleTracked   Lifecycle = "tracked"   // must close, watched by watchdog
	LifecycleUntracked Lifecycle = "untracked" // no watchdog
)

// cardMeta is the registry. Keep entries alphabetised by kind for review
// hygiene (CI can verify).
var cardMeta = map[CardKind]CardMeta{
	CardAgent: {
		DefaultIcon:     "agent",
		DefaultRole:     RoleWorker,
		TitleTpl:        "{name}",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 600_000, // 10 min
	},
	CardArtifact: {
		DefaultIcon: "artifact",
		DefaultRole: RoleWorker,
		TitleTpl:    "{name}",
		Lifecycle:   LifecycleUntracked, // artifacts are emitted as completed events
	},
	CardBudget: {
		DefaultIcon: "warning",
		DefaultRole: RoleSystem,
		TitleTpl:    "预算提醒",
		Lifecycle:   LifecycleUntracked,
	},
	CardMemoryOp: {
		DefaultIcon: "memory",
		DefaultRole: RoleSystem,
		TitleTpl:    "记忆操作",
		Lifecycle:   LifecycleUntracked,
	},
	CardMessage: {
		DefaultIcon:     "chat",
		DefaultRole:     RolePersona,
		TitleTpl:        "回复",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 300_000, // 5 min
	},
	CardPlan: {
		DefaultIcon:     "plan",
		DefaultRole:     RoleOrchestrator,
		TitleTpl:        "执行计划",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 600_000,
	},
	CardStep: {
		DefaultIcon: "dispatch",
		DefaultRole: RoleOrchestrator,
		TitleTpl:    "派出 {subagent_type}",
		Lifecycle:   LifecycleTracked,
		// Step cards receive heartbeats from their dispatched sub-agent:
		// SubAgentStart carries ParentStepID, the translator parents the
		// CardAgent under the step card, and any inner activity
		// (tool_start / append / close) propagates Tracker.Touch up the
		// chain. 5 min is the upper bound for "step opened but no
		// activity ever happened" — covers a wedged dispatch / very slow
		// sub-agent startup without killing legitimate long-running
		// work.
		OrphanTimeoutMs: 300_000, // 5 min
	},
	CardTeam: {
		DefaultIcon: "team",
		DefaultRole: RoleSystem,
		TitleTpl:    "团队",
		Lifecycle:   LifecycleUntracked,
	},
	CardThinking: {
		DefaultIcon:     "thinking",
		DefaultRole:     RolePersona,
		TitleTpl:        "思考",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 300_000,
	},
	CardTodo: {
		DefaultIcon: "task",
		DefaultRole: RoleSystem,
		TitleTpl:    "待办",
		Lifecycle:   LifecycleUntracked, // todos are managed via add/set, not lifecycle
	},
	CardTool: {
		DefaultIcon:     "tool",
		DefaultRole:     RoleWorker,
		TitleTpl:        "{name}",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 120_000,
	},
	CardTurn: {
		DefaultIcon:     "chat",
		DefaultRole:     RolePersona,
		TitleTpl:        "对话第 {turn_no} 轮",
		Lifecycle:       LifecycleTracked,
		OrphanTimeoutMs: 600_000,
	},
}

// LookupCardMeta returns the registered metadata for kind, or a fallback
// for unknown kinds. The fallback is permissive (Untracked, default icon)
// so that callers introducing a new kind without registry update at most
// lose default Hint templates — they don't crash the pipeline.
func LookupCardMeta(kind CardKind) CardMeta {
	if m, ok := cardMeta[kind]; ok {
		return m
	}
	return CardMeta{
		DefaultIcon: "default",
		DefaultRole: RoleSystem,
		TitleTpl:    string(kind),
		Lifecycle:   LifecycleUntracked,
	}
}

// OrphanTimeout returns the watchdog deadline for kind, or 0 if untracked.
func OrphanTimeout(kind CardKind) time.Duration {
	m := LookupCardMeta(kind)
	if m.Lifecycle != LifecycleTracked {
		return 0
	}
	return time.Duration(m.OrphanTimeoutMs) * time.Millisecond
}

// ErrorTypeMeta describes per-error-type defaults.
type ErrorTypeMeta struct {
	DefaultUserMessage string
	DefaultRetryable   bool
}

// errorTypeMeta is the registry of error-type defaults. NewError() and
// the orphan watchdog use these to fill UserMessage/Retryable so callers
// don't repeat boilerplate.
var errorTypeMeta = map[ErrorType]ErrorTypeMeta{
	ErrorTypeToolTimeout: {
		DefaultUserMessage: "执行慢了点，我换个方式重试",
		DefaultRetryable:   true,
	},
	ErrorTypeOrphanTimeout: {
		DefaultUserMessage: "执行超时了，我得放弃这步",
		DefaultRetryable:   false,
	},
	ErrorTypeRateLimit: {
		DefaultUserMessage: "请求太频繁了，稍后重试",
		DefaultRetryable:   true,
	},
	ErrorTypeOverloaded: {
		DefaultUserMessage: "服务繁忙，稍后再试",
		DefaultRetryable:   true,
	},
	ErrorTypeContractFail: {
		DefaultUserMessage: "我没按要求交付，正在补救",
		DefaultRetryable:   true,
	},
	ErrorTypeDependencyFail: {
		DefaultUserMessage: "前置步骤失败，我换个方式",
		DefaultRetryable:   true,
	},
	ErrorTypeUserAborted: {
		DefaultUserMessage: "已取消",
		DefaultRetryable:   false,
	},
	ErrorTypePermissionDenied: {
		DefaultUserMessage: "权限不足，无法完成",
		DefaultRetryable:   false,
	},
	ErrorTypeMaxTurns: {
		DefaultUserMessage: "尝试次数太多了，我先停下",
		DefaultRetryable:   false,
	},
	ErrorTypeContextExceeded: {
		DefaultUserMessage: "上下文太长了，我需要分段处理",
		DefaultRetryable:   false,
	},
	ErrorTypeModelError: {
		DefaultUserMessage: "模型暂时出问题了",
		DefaultRetryable:   true,
	},
	ErrorTypeBudgetExhausted: {
		DefaultUserMessage: "本次预算已耗尽",
		DefaultRetryable:   false,
	},
	ErrorTypeInvalidInput: {
		DefaultUserMessage: "输入有问题，无法继续",
		DefaultRetryable:   false,
	},
	ErrorTypeInternal: {
		DefaultUserMessage: "出了点意外情况",
		DefaultRetryable:   false,
	},
}

// LookupErrorMeta returns the registered metadata for typ; falls back to
// ErrorTypeInternal's defaults for unknown values so unknown errors still
// surface a user-friendly message.
func LookupErrorMeta(typ ErrorType) ErrorTypeMeta {
	if m, ok := errorTypeMeta[typ]; ok {
		return m
	}
	return errorTypeMeta[ErrorTypeInternal]
}

// SeverityForClose returns the severity to attach to a card.close envelope
// based on the close status. Producers may override via WithSeverity.
func SeverityForClose(status Status) Severity {
	switch status {
	case StatusFailed:
		return SeverityError
	case StatusSkipped, StatusCancelled:
		return SeverityWarn
	default:
		return SeverityInfo
	}
}
