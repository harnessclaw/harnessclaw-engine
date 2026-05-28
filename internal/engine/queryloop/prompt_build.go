package queryloop

import (
	"context"
	"os"
	"runtime"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// GetSkillListing returns the cached skill listing string.
// Computed once on first call using FormatCommandsWithinBudget (lazy init).
// The listing is passed into PromptContext.SkillListing for the SkillsSection to render.
func (r *Runner) GetSkillListing() string {
	r.skillListingOnce.Do(func() {
		cmdRegistry := r.deps.CmdRegistry()
		if cmdRegistry == nil {
			return
		}
		cmds := cmdRegistry.GetSkillToolCommands()
		if len(cmds) == 0 {
			return
		}
		// Use 200k context window as default budget reference.
		r.skillListing = skilltool.FormatCommandsWithinBudget(cmds, 200000)
		r.deps.Logger().Info("skill listing generated for injection",
			zap.Int("skill_count", len(cmds)),
			zap.Int("listing_len", len(r.skillListing)),
		)
	})
	return r.skillListing
}

// GetSkillListingFiltered returns the skill listing filtered by an allowed set.
// When allowedSkills is nil, returns the full listing (same as GetSkillListing).
// When non-nil, only skills whose names are in the map are included.
func (r *Runner) GetSkillListingFiltered(allowedSkills map[string]bool) string {
	if allowedSkills == nil {
		return r.GetSkillListing()
	}
	cmdRegistry := r.deps.CmdRegistry()
	if cmdRegistry == nil {
		return ""
	}
	allCmds := cmdRegistry.GetSkillToolCommands()
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

// BuildSystemPrompt constructs the system prompt using the prompt builder.
// Uses per-session whole-output caching: only rebuilds when inputs change.
// Falls back to config.SystemPrompt if builder fails.
func (r *Runner) BuildSystemPrompt(ctx context.Context, sess *session.Session, messages []types.Message) string {
	_ = ctx // currently unused; retained for future cancellation hooks

	promptBuilder := r.deps.PromptBuilder()
	cfg := r.deps.PromptConfig()
	logger := r.deps.Logger()

	// If prompt builder is not initialized, use static prompt
	if promptBuilder == nil {
		return cfg.SystemPrompt
	}

	// Estimate tokens used by conversation
	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	// Compute budget early for cache comparison
	budget := prompt.ComputeSystemPromptBudget(200000, totalTokens, 16384, prompt.DefaultSafetyMargin)

	// Check if we have a valid cached prompt for this session
	cached := sess.PromptCache()

	if cached != nil {
		// Determine if cache is still valid:
		// - budget hasn't dropped by more than 10% (no section would be skipped)
		// - task state hasn't changed
		// - memory hasn't changed
		// - date hasn't changed (cross-midnight)
		today := time.Now().Format("2006-01-02")
		budgetDrift := float64(cached.Budget-budget) / float64(cached.Budget)
		hasTask := false // TODO: populate from session metadata when available
		memoryLen := 0   // TODO: populate when memory loading is implemented

		if budgetDrift < 0.1 && cached.HasTask == hasTask && cached.MemoryLen == memoryLen && cached.Date == today {
			logger.Debug("prompt cache hit",
				zap.String("session_id", sess.ID),
				zap.String("version", cached.Output.(*prompt.PromptOutput).Version),
				zap.Int("budget_cached", cached.Budget),
				zap.Int("budget_current", budget),
			)
			return cached.Prompt
		}

		logger.Debug("prompt cache invalidated",
			zap.String("session_id", sess.ID),
			zap.Float64("budget_drift", budgetDrift),
			zap.Bool("task_changed", cached.HasTask != hasTask),
			zap.Bool("memory_changed", cached.MemoryLen != memoryLen),
			zap.Bool("date_changed", cached.Date != today),
		)
	}

	// Cache miss or invalidated — full build
	promptCtx := &prompt.PromptContext{
		SessionID:         sess.ID,
		Turn:              len(messages),
		Session:           sess,
		Tools:             r.deps.Registry(),
		TotalTokensUsed:   totalTokens,
		ContextWindowSize: cfg.ContextWindow,
		Memory:            make(map[string]string),
		EnvInfo:           r.GetEnvSnapshot(workspace.SessionRoot(workspace.DefaultRootDir(), sess.ID)),
		SkillListing:      r.GetSkillListing(),
		TeamMembers:       r.getTeamMembers(),
	}

	activeProfile := r.deps.PromptProfile()

	output, err := promptBuilder.Build(promptCtx, activeProfile)
	if err != nil {
		logger.Error("prompt build failed, using fallback",
			zap.Error(err),
			zap.String("session_id", sess.ID),
		)
		return cfg.SystemPrompt
	}

	// --- Prompt observability: dump full prompt structure ---
	logger.Debug("========== PROMPT DUMP START ==========")
	logger.Debug(output.Dump())
	logger.Debug("========== PROMPT DUMP END ==========",
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
		logger.Debug("prompt block",
			zap.String("section", b.Name),
			zap.Int("tokens", b.EstimatedTokens),
			zap.Bool("cacheable", b.Cacheable),
			zap.Int("content_len", len(b.Content)),
		)
	}
	for _, s := range output.Metadata.SkippedSections {
		logger.Debug("prompt section skipped",
			zap.String("section", s.Section),
			zap.String("reason", s.Reason),
		)
	}

	// Log the final system prompt text sent to the LLM.
	result := output.ToSystemPrompt()
	logger.Debug("========== FINAL SYSTEM PROMPT START ==========\n"+result+"\n========== FINAL SYSTEM PROMPT END ==========",
		zap.String("session_id", sess.ID),
		zap.Int("char_count", len(result)),
		zap.Int("estimated_tokens", prompt.EstimateTokens(result)),
	)

	// Cache the result for this session
	sess.SetPromptCache(&session.PromptCacheEntry{
		Prompt:    result,
		Output:    output,
		Budget:    budget,
		HasTask:   false, // TODO: update when task state is populated
		MemoryLen: 0,     // TODO: update when memory is populated
		Date:      time.Now().Format("2006-01-02"),
	})

	return result
}

// getTeamMembers builds the dynamic team member list from the agent definition registry.
func (r *Runner) getTeamMembers() []prompt.TeamMember {
	defReg := r.deps.DefRegistry()
	if defReg == nil {
		return nil
	}
	defs := defReg.TeamMembers()
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

// GetEnvSnapshot captures current environment information dynamically.
// sessionRoot, when non-empty, is used as the CWD so agents see the
// session-specific workspace path rather than the generic root.
func (r *Runner) GetEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
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

	// Shell
	if shell := os.Getenv("SHELL"); shell != "" {
		snap.Shell = shell
	} else if comspec := os.Getenv("COMSPEC"); comspec != "" {
		snap.Shell = comspec
	}

	return snap
}
