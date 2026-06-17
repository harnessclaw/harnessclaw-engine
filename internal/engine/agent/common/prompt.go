package common

import (
	"context"

	"harnessclaw-go/internal/engine/agent/definition"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// PromptArgs bundles everything BuildSubAgentPrompt needs to construct
// a sub-agent system prompt. Caller fills relevant fields; unused
// fields are zero-valued and ignored.
//
// The struct intentionally accepts more than the minimal helper
// consumes today — Stages 6/7 (freelancer, scheduler) will iterate
// the body via E2E tests, and callers can pre-populate context now
// without churning their call sites later.
type PromptArgs struct {
	// Ctx is reserved for future use (e.g. cancellation while building
	// prompts that include async lookups). The current implementation
	// does not consume it but accepting it now keeps tier call sites
	// stable.
	Ctx context.Context

	// Session is the sub-agent's own session. SessionID and Session
	// are forwarded into PromptContext when non-nil.
	Session *session.Session

	// Profile selects which prompt sections to render. Required —
	// when nil, the helper returns the fallback identity prompt.
	Profile *prompt.AgentProfile

	// AgentDef is the sub-agent's definition. Reserved for Stages 6/7
	// to drive identity stamping and OutputSchema injection.
	AgentDef *definition.AgentDefinition

	// LeaderDisplayName is the user-facing name of the dispatching
	// agent (e.g. "emma"). Used by tier modules when computing
	// WorkerIdentity for non-leaf coordinators.
	LeaderDisplayName string

	// WorkerDisplayName is the sub-agent's own display name; surfaced
	// in the fallback prompt when the builder is unavailable.
	WorkerDisplayName string

	// SubagentType is the AgentDefinition name (e.g. "developer").
	// Falls back to "<type> 子 agent" identity if AgentDef and
	// WorkerDisplayName are both empty.
	SubagentType string

	// WorkerIdentity, when non-empty, is forwarded to
	// PromptContext.SystemPromptOverride so the role section reflects
	// the caller's identity stamping (BuildFunctionalIdentity output).
	// Tier modules compute this before calling BuildSubAgentPrompt —
	// the helper itself avoids importing the texts package to keep
	// dependency direction one-way.
	WorkerIdentity string

	// LoadedSkillsBlock is the freelancer-specific <loaded-skills> XML
	// container. When non-empty, it is prepended to the rendered
	// prompt with a blank-line separator.
	LoadedSkillsBlock string

	// AllowedSkillsMap is reserved for SkillsSection filtering driven
	// by the caller. Currently unused by the helper directly — the
	// caller pre-filters SkillListing before passing it in.
	AllowedSkillsMap map[string]bool

	// ExpectedOutputs is reserved for an <expected-outputs> block
	// (Stage 6/7 will wire this through once the contract preamble
	// moves into the prompt assembly path).
	ExpectedOutputs []types.ExpectedOutput

	// TaskID and ContextSummary are reserved for distill-mode prompts.
	TaskID         string
	ContextSummary string

	// Builder is the prompt builder. Required — when nil, the helper
	// returns the fallback identity prompt.
	Builder *prompt.Builder

	// EnvSnapshot is forwarded into PromptContext.EnvInfo for the
	// EnvSection (OS / CWD / Shell / Platform / Date).
	EnvSnapshot prompt.EnvSnapshot

	// SkillListing is the pre-rendered text consumed by SkillsSection.
	// Callers should filter by AllowedSkillsMap before passing it in.
	SkillListing string

	// TeamMembers is emma's team for dynamic prompt rendering.
	TeamMembers []prompt.TeamMember

	// ContextWindow is the model's context window size in tokens.
	ContextWindow int

	// Registry is the full tool registry (fallback when AvailableTools
	// is nil).
	Registry *tool.Registry

	// AvailableTools is the actually-callable filtered tool set; the
	// ToolsSection prefers it over Registry.All() so the rendered "#
	// 可用工具" list matches the schemas sent to the LLM.
	AvailableTools []tool.Tool

	// Turn is the conversation turn count forwarded into PromptContext.
	Turn int

	// TotalTokensUsed is forwarded into PromptContext for budgeting
	// decisions inside the builder.
	TotalTokensUsed int
}

// BuildSubAgentPrompt renders the system prompt for a sub-agent using
// the supplied profile, contextual extras, and pre-computed worker
// identity. Tier modules call this to avoid duplicating the
// spawn.buildSubAgentSystemPrompt assembly logic.
//
// When Builder or Profile is nil (or Build returns an error), the
// helper falls back to a minimal identity prompt derived from the
// WorkerDisplayName / SubagentType fields.
//
// LoadedSkillsBlock, when set, is prepended to the rendered output —
// this matches the freelancer convention of placing the
// <loaded-skills> XML container before the rest of the system prompt.
func BuildSubAgentPrompt(args PromptArgs) string {
	if args.Builder == nil || args.Profile == nil {
		return fallbackIdentityPrompt(args)
	}

	pCtx := &prompt.PromptContext{
		Turn:                 args.Turn,
		Tools:                args.Registry,
		AvailableTools:       args.AvailableTools,
		ContextWindowSize:    args.ContextWindow,
		TotalTokensUsed:      args.TotalTokensUsed,
		EnvInfo:              args.EnvSnapshot,
		SkillListing:         args.SkillListing,
		TeamMembers:          args.TeamMembers,
		SystemPromptOverride: args.WorkerIdentity,
		Memory:               map[string]string{},
	}
	if args.Session != nil {
		pCtx.SessionID = args.Session.ID
		pCtx.Session = args.Session
	}

	out, err := args.Builder.Build(pCtx, args.Profile)
	if err != nil {
		return fallbackIdentityPrompt(args)
	}
	rendered := out.ToSystemPrompt()

	// Prepend LoadedSkillsBlock (freelancer specifically).
	if args.LoadedSkillsBlock != "" {
		rendered = args.LoadedSkillsBlock + "\n\n" + rendered
	}
	return rendered
}

// fallbackIdentityPrompt returns the minimal identity sentence used
// when the builder is unavailable. Prefers WorkerDisplayName, falls
// back to SubagentType, then a generic English line.
func fallbackIdentityPrompt(args PromptArgs) string {
	if args.WorkerDisplayName != "" {
		return "你是 " + args.WorkerDisplayName + "。"
	}
	if args.SubagentType != "" {
		return "你是 " + args.SubagentType + " 子 agent。"
	}
	return "You are a sub-agent."
}
