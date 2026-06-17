package emma

import (
	"context"

	"go.uber.org/zap"

	"harnessclaw-go/internal/commands"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tools/builtin/skilltool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// getSkillListing returns the cached skill listing string, computed once
// on first call. The listing is passed into PromptContext.SkillListing
// for the SkillsSection to render.
func (e *Engine) getSkillListing() string {
	e.skillListingOnce.Do(func() {
		if e.cmdRegistry == nil {
			return
		}
		cmds := e.cmdRegistry.GetSkillToolCommands()
		if len(cmds) == 0 {
			return
		}
		e.skillListing = skilltool.FormatCommandsWithinBudget(cmds, 200000)
		e.logger.Info("skill listing generated for injection",
			zap.Int("skill_count", len(cmds)),
			zap.Int("listing_len", len(e.skillListing)),
		)
	})
	return e.skillListing
}

// getSkillListingFiltered returns the skill listing filtered by an allowed
// set. nil means return full listing.
func (e *Engine) getSkillListingFiltered(allowedSkills map[string]bool) string {
	if allowedSkills == nil {
		return e.getSkillListing()
	}
	if e.cmdRegistry == nil {
		return ""
	}
	allCmds := e.cmdRegistry.GetSkillToolCommands()
	if len(allCmds) == 0 {
		return ""
	}
	filtered := make([]*command.PromptCommand, 0, len(allowedSkills))
	for _, cmd := range allCmds {
		if allowedSkills[cmd.Name] {
			filtered = append(filtered, cmd)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return skilltool.FormatCommandsWithinBudget(filtered, 200000)
}

// buildSystemPrompt constructs the system prompt using the prompt builder.
// Falls back to config.SystemPrompt if the builder fails.
//
// After the loop-migration refactor this is called once per ProcessMessage
// (from emma/runner.go's run() entry point), not per turn. The static
// result is passed into loop.Config.SystemPrompt and reused across the
// whole query — which keeps the upstream prompt cache hot.
//
// The per-session cache hit/invalidate logic that used to live here was
// removed because (a) the single call site obviates re-entry caching and
// (b) the cache invalidation criteria (budget drift, date rollover) were
// known to fight Anthropic's prompt cache when triggered mid-query.
func (e *Engine) buildSystemPrompt(ctx context.Context, sess *session.Session, messages []types.Message) string {
	_ = ctx

	if e.promptBuilder == nil {
		return e.config.SystemPrompt
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	promptCtx := &prompt.PromptContext{
		SessionID:         sess.ID,
		Turn:              len(messages),
		Session:           sess,
		Tools:             e.registry,
		TotalTokensUsed:   totalTokens,
		ContextWindowSize: e.contextWindow(),
		Memory:            make(map[string]string),
		EnvInfo:           e.getEnvSnapshot(workspace.SessionRoot(workspace.DefaultRootDir(), sess.ID)),
		SkillListing:      e.getSkillListing(),
		TeamMembers:       e.getTeamMembers(),
	}

	output, err := e.promptBuilder.Build(promptCtx, e.promptProfile)
	if err != nil {
		e.logger.Error("prompt build failed, using fallback",
			zap.Error(err),
			zap.String("session_id", sess.ID),
		)
		return e.config.SystemPrompt
	}

	e.logger.Debug("========== PROMPT DUMP START ==========")
	e.logger.Debug(output.Dump())
	e.logger.Debug("========== PROMPT DUMP END ==========",
		zap.String("session_id", sess.ID),
		zap.Int("turn", promptCtx.Turn),
		zap.String("version", output.Version),
		zap.Int("total_tokens", output.Metadata.TotalTokens),
		zap.Int("budget", output.Metadata.TokenBudget),
		zap.Int("block_count", len(output.Blocks)),
		zap.Int("skipped_count", len(output.Metadata.SkippedSections)),
		zap.Float64("cacheable_ratio", output.Metadata.CacheMetrics.CacheableRatio),
	)
	for _, b := range output.Blocks {
		e.logger.Debug("prompt block",
			zap.String("section", b.Name),
			zap.Int("tokens", b.EstimatedTokens),
			zap.Bool("cacheable", b.Cacheable),
			zap.Int("content_len", len(b.Content)),
		)
	}
	for _, s := range output.Metadata.SkippedSections {
		e.logger.Debug("prompt section skipped",
			zap.String("section", s.Section),
			zap.String("reason", s.Reason),
		)
	}

	result := output.ToSystemPrompt()
	e.logger.Debug("========== FINAL SYSTEM PROMPT START ==========\n"+result+"\n========== FINAL SYSTEM PROMPT END ==========",
		zap.String("session_id", sess.ID),
		zap.Int("char_count", len(result)),
		zap.Int("estimated_tokens", prompt.EstimateTokens(result)),
	)

	return result
}

// getTeamMembers builds the dynamic team member list from the agent
// definition registry.
func (e *Engine) getTeamMembers() []prompt.TeamMember {
	if e.defRegistry == nil {
		return nil
	}
	defs := e.defRegistry.TeamMembers()
	members := make([]prompt.TeamMember, 0, len(defs))
	for _, d := range defs {
		members = append(members, prompt.TeamMember{
			DisplayName: d.DisplayName,
			CodeName:    d.Name,
			Description: d.Description,
			Personality: d.Personality,
			Triggers:    d.Triggers,
		})
	}
	return members
}

// getEnvSnapshot captures current environment information dynamically.
// sessionRoot, when non-empty, is used as the CWD so agents see the
// session-specific workspace path rather than the generic root.
//
// 实现委托给 prompt.BuildEnvSnapshot —— 让 emma 主路径和 sub-agent dispatch
// 路径（loop/runtime/llm.go）共享同一份逻辑，不再各写一遍。
func (e *Engine) getEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	return prompt.BuildEnvSnapshot(sessionRoot)
}
