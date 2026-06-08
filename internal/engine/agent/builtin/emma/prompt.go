package emma

import (
	"context"
	"os"
	"runtime"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/commands"
	"harnessclaw-go/internal/legacy/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tools/builtin/skilltool"
	"harnessclaw-go/internal/legacy/workspace"
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
// Uses per-session whole-output caching; falls back to config.SystemPrompt
// if the builder fails.
func (e *Engine) buildSystemPrompt(ctx context.Context, sess *session.Session, messages []types.Message) string {
	_ = ctx

	if e.promptBuilder == nil {
		return e.config.SystemPrompt
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	budget := prompt.ComputeSystemPromptBudget(200000, totalTokens, 16384, prompt.DefaultSafetyMargin)

	cached := sess.PromptCache()
	if cached != nil {
		today := time.Now().Format("2006-01-02")
		budgetDrift := float64(cached.Budget-budget) / float64(cached.Budget)
		hasTask := false
		memoryLen := 0

		if budgetDrift < 0.1 && cached.HasTask == hasTask && cached.MemoryLen == memoryLen && cached.Date == today {
			e.logger.Debug("prompt cache hit",
				zap.String("session_id", sess.ID),
				zap.String("version", cached.Output.(*prompt.PromptOutput).Version),
				zap.Int("budget_cached", cached.Budget),
				zap.Int("budget_current", budget),
			)
			return cached.Prompt
		}

		e.logger.Debug("prompt cache invalidated",
			zap.String("session_id", sess.ID),
			zap.Float64("budget_drift", budgetDrift),
			zap.Bool("task_changed", cached.HasTask != hasTask),
			zap.Bool("memory_changed", cached.MemoryLen != memoryLen),
			zap.Bool("date_changed", cached.Date != today),
		)
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

	sess.SetPromptCache(&session.PromptCacheEntry{
		Prompt:    result,
		Output:    output,
		Budget:    budget,
		HasTask:   false,
		MemoryLen: 0,
		Date:      time.Now().Format("2006-01-02"),
	})

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
func (e *Engine) getEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	snap := prompt.EnvSnapshot{
		OS:       runtime.GOOS,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Date:     time.Now().Format("2006-01-02"),
	}

	if sessionRoot != "" {
		snap.CWD = sessionRoot
	} else {
		snap.CWD = "~/.harnessclaw/workspace"
	}

	if shell := os.Getenv("SHELL"); shell != "" {
		snap.Shell = shell
	} else if comspec := os.Getenv("COMSPEC"); comspec != "" {
		snap.Shell = comspec
	}

	return snap
}
