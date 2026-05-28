package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
	schedspec "harnessclaw-go/internal/engine/scheduler/spec"
	schedulertypes "harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// workspaceRootDir is a package-local alias of workspace.DefaultRootDir
// so engine internals can reach the shared default without each call site
// re-importing the workspace package.
func workspaceRootDir() string {
	return workspace.DefaultRootDir()
}

// planWriterRegistry returns the process-level registry — see
// workspace.DefaultPlanWriterRegistry for invariants. Local alias keeps
// engine call sites tidy.
func planWriterRegistry() *workspace.PlanWriterRegistry {
	return workspace.DefaultPlanWriterRegistry()
}

// deriveSessionRoot computes the {workspace}/session/{root-session-id}
// path for a spawn. Falls back to ParentSessionID when RootSessionID is
// unset (the L2 case where the parent IS the root). Returns "" if no
// session id is available — engine still works, just without the
// SessionRoot hint in AgentScope.
func deriveSessionRoot(cfg *agent.SpawnConfig) string {
	rootSID := cfg.RootSessionID
	if rootSID == "" {
		rootSID = cfg.ParentSessionID
	}
	if rootSID == "" {
		return ""
	}
	root := workspaceRootDir()
	if root == "" {
		return ""
	}
	return workspace.SessionRoot(root, rootSID)
}

// maxSubAgentTurns is the hard upper limit for any sub-agent's MaxTurns,
// regardless of what SpawnConfig requests. 30 leaves room for genuine
// multi-tool research-and-write workloads (which often need 15-25 turns)
// without removing the runaway-loop safety net entirely. Hitting this
// cap surfaces as TerminalMaxTurns and routes through the same failure
// gate as other step failures.
const maxSubAgentTurns = 30

// SpawnSync implements agent.AgentSpawner. It creates an ephemeral sub-agent
// session and runs a full query loop synchronously, blocking until completion.
//
// The 14-step flow follows the design doc §3.7:
//  1. Apply timeout
//  2. Cap MaxTurns
//  3. Generate sub-session ID
//  4. Create sub-session with metadata
//  5. Initialize conversation context (spawn vs fork)
//  6. Build filtered ToolPool
//  7. Resolve prompt profile
//  8. Build permission checker (InheritedChecker)
//  9. Create drain channel
//  10. Emit subagent.start on parent out (via eventBus)
//  11. Run query loop
//  12. Collect output
//  13. Emit subagent.end
//  14. Return SpawnResult
func (s *Spawner) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (result *agent.SpawnResult, err error) {
	if cfg.Name == "" {
		cfg.Name = cfg.SubagentType
	}
	agentID := "agent_" + uuid.New().String()[:8]
	sessionID := cfg.ParentSessionID + "_sub_" + uuid.New().String()[:8]
	startTime := time.Now()

	logger := s.deps.Logger().With(
		zap.String("agent_id", agentID),
		zap.String("sub_session_id", sessionID),
		zap.String("parent_session_id", cfg.ParentSessionID),
		zap.String("subagent_type", cfg.SubagentType),
		zap.Bool("fork", cfg.Fork),
	)

	// Panic recovery: convert panics to error result.
	defer func() {
		if r := recover(); r != nil {
			logger.Error("sub-agent panicked", zap.Any("panic", r))
			terminal := types.Terminal{
				Reason:  types.TerminalModelError,
				Message: fmt.Sprintf("internal error: sub-agent crashed: %v", r),
			}
			result = &agent.SpawnResult{
				Output:    fmt.Sprintf("internal error: sub-agent crashed: %v", r),
				Terminal:  &terminal,
				Usage:     &types.Usage{},
				SessionID: sessionID,
				AgentID:   agentID,
			}
			err = nil
		}
	}()

	logger.Info("spawning synchronous sub-agent")

	// DEBUG: spawn.start data-flow snapshot — what just came in over the
	// dispatch boundary. Counts only (no full prompt body) so the line
	// stays grep-friendly; full prompt is dumped via the LLM-request
	// debug line a few turns later. Pair with `spawn.end` below to see
	// both ends of one sub-agent execution.
	logger.Debug("spawn.start",
		zap.String("agent_id", agentID),
		zap.String("subagent_type", cfg.SubagentType),
		zap.String("name", cfg.Name),
		zap.String("description", cfg.Description),
		zap.Int("prompt_len", len(cfg.Prompt)),
		zap.String("prompt_preview", truncateForLog(cfg.Prompt, 200)),
		zap.Int("expected_outputs", len(cfg.ExpectedOutputs)),
		zap.Int("required_outputs", countRequired(cfg.ExpectedOutputs)),
		zap.String("task_id", cfg.TaskID),
		zap.Bool("fork", cfg.Fork),
		zap.Int("parent_messages", len(cfg.ParentMessages)),
		zap.Int("context_summary_len", len(cfg.ContextSummary)),
		zap.Int("allowed_skills", len(cfg.AllowedSkills)),
	)

	// Step 1: Apply timeout.
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	// Stats ctx keys: every LLM call this sub-agent makes routes its
	// usage through StatsProvider, which reads session_id + agent_run_id
	// + immediate_parent + root from ctx. session_id is THIS sub-agent's
	// own sub_session — that lets stats_provider keep a per-layer tracker
	// so an L2 agent's metrics include its L3 children's tokens (the L2
	// tracker is the L3's immediate-parent write target).
	ctx = sessionstats.WithSessionID(ctx, sessionID)
	ctx = sessionstats.WithAgentRunID(ctx, agentID)

	// Root session — empty cfg.RootSessionID means "I am at L2, my parent
	// is the root". Propagating via ctx so dual-tracker writes in
	// stats_provider see it without changing every cfg.X plumbing site.
	rootSID := cfg.RootSessionID
	if rootSID == "" {
		rootSID = cfg.ParentSessionID
	}
	ctx = sessionstats.WithRootSessionID(ctx, rootSID)
	// Immediate parent: the L2 sub_session for an L3 agent, or the emma
	// session for an L2 agent. stats_provider uses this to dual-write into
	// the layer directly above so each non-root tracker accumulates its
	// own subtree.
	ctx = sessionstats.WithImmediateParentSessionID(ctx, cfg.ParentSessionID)

	// Idempotent fallback: ensure the on-disk task dir exists before any
	// sub-agent tool can be dispatched. Real L2 callers will already have
	// created it via plan_update, but ad-hoc dispatches and tests still
	// land here without the dir. workspace.EnsureTaskDir is a no-op when
	// the dir already exists. Skipped when there's no task_id / no root
	// session (legacy / freeform spawn) — nothing safe to anchor against.
	if cfg.TaskID != "" && rootSID != "" {
		if root := workspaceRootDir(); root != "" {
			if err := workspace.EnsureTaskDir(root, rootSID, cfg.TaskID); err != nil {
				return nil, fmt.Errorf("ensure task dir: %w", err)
			}
		}
	}

	// Step 2: Cap MaxTurns.
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = s.deps.SpawnerConfig().MaxTurns / 2
		if maxTurns < 5 {
			maxTurns = 5
		}
	}
	if maxTurns > maxSubAgentTurns {
		maxTurns = maxSubAgentTurns
	}

	// Step 3-4: Create sub-session with metadata.
	sess := &session.Session{
		ID:        sessionID,
		State:     session.StateActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata: map[string]any{
			"parent_session_id": cfg.ParentSessionID,
			"agent_type":        string(cfg.AgentType),
			"is_sub_agent":      true,
			"fork":              cfg.Fork,
			"agent_id":          agentID,
		},
	}

	// Look up the AgentDefinition early so the preamble composer can render
	// the per-definition sub-agent contract (OutputSchema / Skills /
	// Limitations) for TierSubAgent. The lookup is reused below for tool
	// pool filtering and profile resolution, so this is just hoisting it.
	var agentDef *agent.AgentDefinition
	if defReg := s.deps.DefRegistry(); defReg != nil && cfg.SubagentType != "" {
		agentDef = defReg.Get(cfg.SubagentType)
	}

	// InputSchema validation: when the definition declares an input contract
	// AND the caller provided structured inputs, validate before any LLM call.
	// A mismatch means the dispatcher constructed a bad request — fail fast
	// rather than letting the sub-agent guess from a partial prompt.
	if agentDef != nil && len(agentDef.InputSchema) > 0 && len(cfg.Inputs) > 0 {
		if fails := submittool.ValidateAgainstSchema(agentDef.InputSchema, cfg.Inputs); len(fails) > 0 {
			return nil, fmt.Errorf("input schema validation failed for %q: %s",
				cfg.SubagentType, strings.Join(fails, "; "))
		}
	}

	// Skill-aware hydration: any L3 that declares the skill self-mgmt
	// tools (search_skill / load_skill / unload_skill / list_loaded_skills)
	// gets a SkillTracker so those tools have somewhere to store state.
	//
	// freelancer additionally accepts `candidate_skills` from L2 — those
	// get preloaded into the tracker and prepended to cfg.Prompt as a
	// <loaded-skills> block. Fixed L3 agents (writer / developer / ...)
	// that have been enhanced with skill tools just receive an empty
	// tracker and use search_skill / load_skill at runtime themselves.
	var freelancerTracker *loop.SkillTracker
	if agentDef != nil && defHasSkillSelfMgmtTool(agentDef.AllowedTools) {
		candidates := parseCandidateSkills(cfg.Inputs)
		tracker, newPrompt, err := hydrateFreelancer(s.deps.SkillReader(), s.deps.BuildLoadedSkillsBlock, candidates, cfg.Prompt)
		if err != nil {
			return nil, fmt.Errorf("skill hydration failed for %q: %w", cfg.SubagentType, err)
		}
		freelancerTracker = tracker
		cfg.Prompt = newPrompt

		// Observability: structured log captures which skills got preloaded
		// (if any) and which agent type is running. Operators can answer
		// "did my agent pick up the skill I just installed?" without
		// enabling provider-level request dumps.
		active, _ := tracker.List()
		loaded := make([]map[string]string, 0, len(active))
		for _, r := range active {
			loaded = append(loaded, map[string]string{
				"name":    r.Name,
				"version": r.Version,
				"source":  string(r.Source),
			})
		}
		logger.Info("skill-aware agent spawn",
			zap.String("agent_id", agentID),
			zap.String("subagent_type", cfg.SubagentType),
			zap.Int("candidate_count", len(candidates)),
			zap.Any("loaded_skills", loaded),
		)
	}

	// Step 5a: Compose <task-inputs> preamble. Under the local-files-as-
	// truth model the framework lists upstream files (with summaries from
	// each dep's meta.json) instead of trace-scoped artifact metadata.
	// L3 calls FileRead on the listed paths when it needs the contents.
	// Empty cfg.InputPaths → no preamble (the artifact-store legacy path
	// also returned empty in the common single-task case, so this is a
	// drop-in shape).
	artPreamble := composeTaskInputsPreamble(cfg, logger)

	// Step 5a': Render the per-spawn deliverable contract (doc §3 M1).
	// Empty when the dispatcher didn't supply ExpectedOutputs, in which
	// case we don't add anything — keeps simple-task prompts identical
	// to the legacy path.
	contractPreamble := agent.RenderExpectedOutputs(cfg.ExpectedOutputs)

	// Step 5a'': Render the per-definition sub-agent contract (TierSubAgent
	// only). This carries the agent's PERMANENT contract — OutputSchema,
	// Skills, Limitations — distinct from the per-spawn ExpectedOutputs.
	// Without this preamble the L3 LLM has no way to learn its own output
	// shape from the registry, so it would either guess (often wrong) or
	// terminate on plain end_turn (rejected by the driver as not-yet-done).
	subAgentPreamble := agent.RenderSubAgentContract(agentDef)

	// Step 5a''': <spawn-info> block — the only per-spawn block in the
	// preamble. Gives the LLM its task_id + task_dir directly so it can
	// route write/edit calls inside write_scope and pass the right
	// meta_path to submit_task_result without guessing. Skipped when we
	// don't have both task_id and a session root (legacy/freeform spawn).
	spawnInfoPreamble := ""
	if cfg.TaskID != "" && rootSID != "" {
		if root := workspaceRootDir(); root != "" {
			taskDir := workspace.TaskDir(root, rootSID, cfg.TaskID)
			spawnInfoPreamble = "<spawn-info>\n" +
				"task_id: " + cfg.TaskID + "\n" +
				"task_dir: " + taskDir + "\n" +
				"session_id: " + rootSID + "\n" +
				"meta_path (传给 submit_task_result): tasks/" + cfg.TaskID + "/meta.json\n" +
				"\n所有产物文件写到 task_dir/ 下；meta_write 与 submit_task_result 会按 ctx 自动校验。\n" +
				"</spawn-info>"
		}
	}

	// composeUserMessage stacks the framework-injected blocks above the
	// per-mode body. Order matters — the LLM reads top-down:
	//   1. <available-artifacts>     — what the L3 may consume
	//   2. <sub-agent-contract>      — who I am + what shape I always produce
	//   3. <expected-outputs>        — what THIS task additionally requires
	//   4. <spawn-info>              — my workspace coordinates (per-spawn)
	//   5. <task> ... </task>        — the actual instruction
	// Identity / contract come before task-specific overlay, matching the
	// "general before specific" prompt-cache stability principle.
	composeUserMessage := func(body string) string {
		var parts []string
		if artPreamble != "" {
			parts = append(parts, artPreamble)
		}
		if subAgentPreamble != "" {
			parts = append(parts, subAgentPreamble)
		}
		if contractPreamble != "" {
			parts = append(parts, contractPreamble)
		}
		if spawnInfoPreamble != "" {
			parts = append(parts, spawnInfoPreamble)
		}
		parts = append(parts, body)
		return joinNonEmpty(parts, "\n\n")
	}

	// Step 5b: Initialize conversation context.
	// Three modes:
	//   Fork:    full parent history + new prompt (maximum context, risk of attention dilution)
	//   Distill: compressed summary + new prompt (balanced: relevant context without noise)
	//   Spawn:   blank session + new prompt (minimum context, maximum focus)
	var systemPromptOverride string
	if cfg.Fork && len(cfg.ParentMessages) > 0 {
		// Fork mode: copy parent conversation and append new prompt.
		for _, pm := range cfg.ParentMessages {
			sess.AddMessage(types.Message{
				Role: types.Role(pm.Role),
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: pm.Content,
				}},
				CreatedAt: time.Now(),
			})
		}
		// In fork mode the new task message is a fresh user turn; wrap
		// it with <task> so the LLM doesn't confuse framework preamble
		// blocks (artifact list / contract / inputs) with the inherited
		// conversation.
		taskBody := "<task>\n" + cfg.Prompt + "\n</task>"
		taskBody = composeUserMessage(taskBody)
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: taskBody,
			}},
			CreatedAt: time.Now(),
		})
		systemPromptOverride = cfg.SystemPromptOverride
	} else if cfg.ContextSummary != "" {
		// Distill mode: inject compressed context + task prompt as a single user
		// message. Using one message avoids wasting tokens on a synthetic assistant
		// turn and prevents the false-confirmation bias that "I understand" creates.
		// The XML tags let the model distinguish background from task instruction.
		distillPrompt := "<context-summary>\n" + cfg.ContextSummary + "\n</context-summary>\n\n<task>\n" + cfg.Prompt + "\n</task>"
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: composeUserMessage(distillPrompt),
			}},
			CreatedAt: time.Now(),
		})
	} else {
		// Spawn mode: blank session with just the prompt.
		// Wrap with <task> whenever the framework prepends ANY block —
		// either the artifact preamble or the expected-outputs contract.
		// Otherwise the LLM may treat the closing list as part of its
		// own instructions.
		body := cfg.Prompt
		if artPreamble != "" || contractPreamble != "" {
			body = "<task>\n" + cfg.Prompt + "\n</task>"
		}
		sess.AddMessage(types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{{
				Type: types.ContentTypeText,
				Text: composeUserMessage(body),
			}},
			CreatedAt: time.Now(),
		})
	}

	// Step 6: Build filtered ToolPool.
	//
	// Filtering policy:
	//   - If AgentDefinition.AllowedTools is non-empty, treat it as an
	//     authoritative whitelist that bypasses AgentType blacklist.
	//     This lets specialised agents like "scheduler" (L2) re-enable
	//     tools that are blanket-blocked for sync sub-agents (e.g. Agent).
	//   - Otherwise apply the default AgentType-based blacklist.
	//
	// agentDef was looked up above (step 5 — we hoisted it so the preamble
	// composer could render the per-definition sub-agent contract).

	pool := tool.NewToolPool(s.deps.Registry(), nil, nil)

	// L3 sub-agents get their AllowedTools (when set) augmented with the
	// always-required terminal tools — submit_task_result and escalate_to_planner.
	// Without this, a worker definition that whitelists ["web_search"]
	// would have no way to submit OR escalate, and the driver would loop
	// to nudge cap and fail. The augment happens BEFORE FilterByNames so
	// the final pool definitively includes both.
	allowedTools := agentDef.MaybeAugmentForSubAgent()

	if len(allowedTools) > 0 {
		// Explicit whitelist — bypass AgentType blacklist entirely.
		pool = pool.FilterByNames(allowedTools)
		logger.Debug("tool pool restricted by agent definition whitelist",
			zap.String("agent", cfg.SubagentType),
			zap.Int("tools", pool.Size()),
			zap.Strings("allowed", allowedTools),
		)
	} else {
		// No whitelist — apply default AgentType blacklist.
		pool = pool.FilteredFor(cfg.AgentType)
	}

	// L3 invariant: dispatch tools are always stripped, even when not in
	// AllowedTools. Defense in depth — Validate now rejects them at
	// registration, but stale stored definitions might predate that check.
	// Only applies to TierSubAgent; RunAsLLMAgent agents manage their own
	// tool whitelist via AllowedTools (see pool restriction block below).
	isSubAgent := agentDef != nil && agentDef.EffectiveTier() == agent.TierSubAgent
	// useSubAgentDriver: routes through the LLM driver instead of SchedulerCoordinator.
	// True for TierSubAgent AND for coordinator-tier agents marked RunAsLLMAgent
	// (plan-agent, plan-executor-agent).
	useSubAgentDriver := isSubAgent || (agentDef != nil && agentDef.RunAsLLMAgent)
	if isSubAgent {
		pool = pool.WithoutNames(dispatchToolNames)
		// P1-5: dangerous tools (Bash etc.) must be opt-in for sub-agents.
		// `keepList` is the agent's declared whitelist — any dangerous
		// tool NOT explicitly named there gets stripped. Effect: a worker
		// with empty AllowedTools (legacy default) has zero dangerous
		// tools regardless of what the AgentType blacklist let through.
		var keepList []string
		if agentDef != nil {
			keepList = agentDef.AllowedTools
		}
		pool = pool.WithoutDangerousUnless(keepList)
	}

	// Search-tool capability gap — emit a one-shot CardSystem notice
	// to the user when this sub-agent declares search capability but
	// neither web_search nor tavily_search is registered at runtime.
	// nil-safe on detector or ParentOut.
	if useSubAgentDriver && s.searchGapDetector != nil {
		var declared []string
		if agentDef != nil {
			declared = agentDef.AllowedTools
		}
		s.searchGapDetector.CheckAndEmit(
			ctx, cfg.ParentSessionID, cfg.SubagentType,
			declared, pool.Names(),
			func(ctx context.Context, ev types.EngineEvent) error {
				if cfg.ParentOut == nil {
					return fmt.Errorf("parent out channel is nil")
				}
				select {
				case cfg.ParentOut <- ev:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		)
	}

	// Step 7: Resolve prompt profile.
	// Priority: AgentDefinition.Profile > subagentType string mapping > WorkerProfile.
	var profile *prompt.AgentProfile
	if agentDef != nil && agentDef.Profile != "" {
		profile = prompt.ResolveProfileByName(agentDef.Profile)
		logger.Debug("profile from agent definition", zap.String("profile", agentDef.Profile))
	}
	if profile == nil {
		profile = resolveSubAgentProfile(cfg.SubagentType)
	}

	// Step 8: Build permission checker.
	// Use InheritedChecker with parent's session-approved tools.
	var permChecker permission.Checker
	approvedTools := s.deps.GetSessionApprovedTools(cfg.ParentSessionID)
	if len(approvedTools) > 0 {
		permChecker = permission.NewInheritedChecker(approvedTools)
	} else {
		permChecker = permission.BypassChecker{}
	}

	// Step 9: Build sub-agent engine config.
	parentCfg := s.deps.SpawnerConfig()
	subConfig := SpawnConfig{
		MaxTurns:             maxTurns,
		AutoCompactThreshold: parentCfg.AutoCompactThreshold,
		ToolTimeout:          parentCfg.ToolTimeout,
		MaxTokens:            parentCfg.MaxTokens,
		ContextWindow:        parentCfg.ContextWindow,
		SystemPrompt:         parentCfg.SystemPrompt,
		ClientTools:          false, // sub-agents always server-side
		MaxPlanReplans:       parentCfg.MaxPlanReplans,
		MaxStepAttempts:      parentCfg.MaxStepAttempts,
		LLMMaxRetries:        parentCfg.LLMMaxRetries,
		LLMAPITimeout:        parentCfg.LLMAPITimeout,
		LLMFirstByteTimeout:  parentCfg.LLMFirstByteTimeout,
	}

	// Build allowed skills map.
	// Priority: SpawnConfig.AllowedSkills > AgentDefinition.Skills > nil (all skills).
	var allowedSkills map[string]bool
	if len(cfg.AllowedSkills) > 0 {
		allowedSkills = make(map[string]bool, len(cfg.AllowedSkills))
		for _, s := range cfg.AllowedSkills {
			allowedSkills[s] = true
		}
	} else if agentDef != nil && len(agentDef.Skills) > 0 {
		allowedSkills = make(map[string]bool, len(agentDef.Skills))
		for _, s := range agentDef.Skills {
			allowedSkills[s] = true
		}
	}

	// Per-agent temperature / OutputSchema flow from registry to loop.
	// Both stay nil for definitions that don't set them, in which case
	// the loop and the submit_task_result validator behave as before.
	var temperature *float64
	var outputSchema map[string]any
	if agentDef != nil {
		temperature = agentDef.Temperature
		outputSchema = agentDef.OutputSchema
	}

	lc := &loopConfig{
		pool:                 pool,
		profile:              profile,
		permChecker:          permChecker,
		config:               subConfig,
		systemPromptOverride: systemPromptOverride,
		subagentType:         cfg.SubagentType,
		allowedSkills:        allowedSkills,
		logger:               logger,
		agentID:              agentID,
		taskID:               cfg.TaskID,
		taskStartedAt:        cfg.TaskStartedAt,
		expectedOutputs:      cfg.ExpectedOutputs,
		temperature:          temperature,
		outputSchema:         outputSchema,
		originalPrompt:       cfg.Prompt,
		skillTracker:         freelancerTracker, // nil for non-freelancer spawns
		readScope:            cfg.ReadScope,
		writeScope:           cfg.WriteScope,
		sessionRoot:          deriveSessionRoot(cfg),
		stripDispatchTools:   isSubAgent,
	}

	// Step 10: Emit subagent.start event.
	//
	// AgentTask carries the full prompt the parent dispatched. The client
	// can render it as "researcher 接到的任务：…" so the user sees what
	// each L3 was actually asked to do — without that, only the 3-5-word
	// AgentDesc reaches the wire and the sub-agent's actual mission is
	// invisible. We truncate at 800 runes so a long context-summary
	// preamble doesn't bloat the wire payload; the sub-agent's own loop
	// still receives the full prompt.
	if cfg.ParentOut != nil {
		// Build LoadedSkills snapshot from the tracker so the wire event
		// carries "this spawn started with X skills preloaded". Empty
		// when the agent isn't skill-aware (non-freelancer + no skill
		// tools in AllowedTools) so the field naturally disappears from
		// the JSON for unrelated workers.
		var loaded []types.LoadedSkillInfo
		if freelancerTracker != nil {
			active, _ := freelancerTracker.List()
			loaded = make([]types.LoadedSkillInfo, 0, len(active))
			for _, r := range active {
				loaded = append(loaded, types.LoadedSkillInfo{
					Name:    r.Name,
					Version: r.Version,
					Source:  string(r.Source),
				})
			}
		}
		cfg.ParentOut <- types.EngineEvent{
			Type:          types.EngineEventSubAgentStart,
			AgentID:       agentID,
			AgentName:     cfg.Name,
			AgentDesc:     cfg.Description,
			AgentTask:     truncateRunes(cfg.Prompt, 800),
			AgentType:     string(cfg.AgentType),
			SubagentType:  cfg.SubagentType, // writer / researcher / freelancer / ...
			LoadedSkills:  loaded,
			ParentAgentID: cfg.ParentSessionID,
			ParentStepID:  cfg.ParentStepID,
		}
	}
	// Tracker hook: register this sub-agent row on the parent session's
	// tracker so the dashboard sees a "running" entry as soon as the
	// start event hits the wire. Skipped silently when stats aren't
	// wired in (tests) or when the parent session id is missing.
	if statsReg := s.deps.StatsRegistry(); statsReg != nil && cfg.ParentSessionID != "" {
		if tr := statsReg.Get(cfg.ParentSessionID); tr != nil {
			tr.StartSubAgent(agentID, agentID, string(cfg.AgentType), cfg.SubagentType, "")
		}
	}
	// Plan B dual-write: when root differs from immediate parent, open a
	// row on the root tracker too so GET /sessions/{root_id}/metrics shows
	// this sub-agent as one of its descendants — flat list of all L1/L2/L3
	// agents, keyed by agentID. Skipped when root == parent (the L2 case).
	if statsReg := s.deps.StatsRegistry(); statsReg != nil && rootSID != "" && rootSID != cfg.ParentSessionID {
		if tr := statsReg.Get(rootSID); tr != nil {
			tr.StartSubAgent(agentID, agentID, string(cfg.AgentType), cfg.SubagentType, "")
			logger.Debug("sub-agent row opened on root tracker too",
				zap.String("root_session", rootSID),
				zap.String("parent_session", cfg.ParentSessionID),
			)
		}
	}
	if bus := s.deps.EventBus(); bus != nil {
		bus.Publish(event.Event{
			Topic: event.TopicSubAgentStarted,
			Payload: map[string]any{
				"agent_id":       agentID,
				"name":           cfg.Name,
				"description":    cfg.Description,
				"subagent_type":  cfg.SubagentType,
				"agent_type":     string(cfg.AgentType),
				"fork":           cfg.Fork,
				"parent_session": cfg.ParentSessionID,
			},
		})
	}

	// Step 11-12: Run query loop, drain events, collect text output.
	// Forward events to ParentOut for real-time client streaming.
	out := make(chan types.EngineEvent, 64)
	var loopResult subAgentLoopResult
	var textBuf strings.Builder
	var cumulativeUsage types.Usage
	var deliverables []types.Deliverable
	// producedArtifacts accumulates Refs from every tool_end event the
	// sub-agent emitted (the executor stamps them when render_hint=artifact).
	// Attached to the subagent_end event so the UI can render one card
	// listing "this sub-agent's outputs" without re-scanning the per-tool
	// stream. See doc §10.
	var producedArtifacts []types.ArtifactRef

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		// TierSubAgent / RunAsLLMAgent → strict LLM ReAct loop (no SchedulerCoordinator).
		// Coordinator tier (without RunAsLLMAgent) → SchedulerCoordinator handles kind selection.
		if useSubAgentDriver {
			loopResult = s.runSubAgentDriver(ctx, sess, lc, out)
		} else {
			sp := schedspec.TaskSpec{
				Goal:      cfg.Prompt,
				Layout:    "flat",
				SessionID: cfg.ParentSessionID,
				Model:     cfg.Model,
			}
			if cfg.CoordinatorMode != "" {
				sp.Hint.Kind = schedulertypes.Kind(cfg.CoordinatorMode)
			}
			ref, runErr := s.deps.SchedulerCoord().Run(ctx, sp, out)
			if runErr != nil {
				loopResult = subAgentLoopResult{
					Terminal: types.Terminal{
						Reason:  types.TerminalModelError,
						Message: runErr.Error(),
					},
				}
			} else {
				loopResult = metaRefToLoopResult(ref, workspaceRootDir(), cfg.ParentSessionID)
				loopResult.CoordinatorMode = cfg.CoordinatorMode
				if loopResult.CoordinatorMode == "" {
					loopResult.CoordinatorMode = "react"
				}
			}
		}
	}()

	for evt := range out {
		switch evt.Type {
		case types.EngineEventText:
			textBuf.WriteString(evt.Text)
		case types.EngineEventToolEnd:
			// Deliverable detection used to look at FileWrite's
			// render_hint=file_info here. Under the local-files-as-truth
			// model promote is the sole Deliverable source — it emits an
			// EngineEventDeliverable directly through the parent channel,
			// so no per-tool-end inspection is needed.
			// Aggregate artifacts the executor stamped on this tool_end
			// (single Ref per ArtifactWrite). Buffered until subagent_end
			// rather than emitted twice — the per-tool emission happens
			// further down when we forward as subagent_event.
			if len(evt.Artifacts) > 0 {
				producedArtifacts = append(producedArtifacts, evt.Artifacts...)
			}
		case types.EngineEventDone:
			if evt.Usage != nil {
				cumulativeUsage = *evt.Usage
			}
		}

		// Forward observability events to the parent's output channel.
		//
		// L1/L2 隔离：sub-agent LLM 文本 (EngineEventText) is intentionally
		// NOT forwarded — only the L1 main agent (emma) generates user-facing
		// prose. The spawning parent reads the sub-agent's output via
		// SpawnResult.Summary in the tool_result and polishes its own reply.
		//
		// Two forwarding paths:
		//
		//  1. THIS sub-agent's own tool lifecycle (ToolStart/ToolEnd) — wrap
		//     as SubAgentEvent stamped with this layer's agentID, so the
		//     parent can render "scheduler is calling task / web_search".
		//
		//  2. Events that already came from a deeper layer (e.g. L3 events
		//     bubbling through L2 on their way to L1) — these arrive here as
		//     SubAgentStart/SubAgentEnd/SubAgentEvent/Deliverable and must
		//     be forwarded *as-is* with their original AgentID preserved.
		//     Without this, the WebSocket client never sees L3 lifecycle when
		//     L2 (scheduler) dispatches L3 via the task tool.
		//
		// See docs/protocols/websocket.md v1.10.
		if cfg.ParentOut != nil {
			switch evt.Type {
			case types.EngineEventAgentIntent:
				// The sub-agent's executor stripped `intent` off the tool
				// input and emitted this — wrap it as subagent_event so it
				// reaches the wire stamped with this layer's agent identity
				// (mirroring how tool_start/tool_end are wrapped). The
				// client renders "researcher 正在搜 vLLM" without needing
				// to dig into the inner ToolInput JSON.
				cfg.ParentOut <- types.EngineEvent{
					Type:      types.EngineEventSubAgentEvent,
					AgentID:   agentID,
					AgentName: cfg.Name,
					SubAgentEvent: &types.SubAgentEventData{
						EventType: "intent",
						ToolName:  evt.ToolName,
						ToolUseID: evt.ToolUseID,
						Intent:    evt.Intent,
					},
				}
			case types.EngineEventToolStart:
				cfg.ParentOut <- types.EngineEvent{
					Type:      types.EngineEventSubAgentEvent,
					AgentID:   agentID,
					AgentName: cfg.Name,
					SubAgentEvent: &types.SubAgentEventData{
						EventType: "tool_start",
						ToolName:  evt.ToolName,
						ToolUseID: evt.ToolUseID,
						ToolInput: evt.ToolInput,
					},
				}
			case types.EngineEventToolEnd:
				inner := &types.SubAgentEventData{
					EventType: "tool_end",
					ToolName:  evt.ToolName,
					ToolUseID: evt.ToolUseID,
				}
				if evt.ToolResult != nil {
					inner.Output = evt.ToolResult.Content
					inner.IsError = evt.ToolResult.IsError
				}
				// Per-tool artifact surfacing (doc §10): if this tool_end
				// carries Refs, forward them at the inner-event level so
				// the UI can light up cards as each ArtifactWrite lands,
				// not only at the aggregated subagent_end.
				if len(evt.Artifacts) > 0 {
					inner.Artifacts = append([]types.ArtifactRef(nil), evt.Artifacts...)
				}
				cfg.ParentOut <- types.EngineEvent{
					Type:          types.EngineEventSubAgentEvent,
					AgentID:       agentID,
					AgentName:     cfg.Name,
					SubAgentEvent: inner,
				}
			case types.EngineEventSubAgentStart,
				types.EngineEventSubAgentEnd,
				types.EngineEventSubAgentEvent,
				types.EngineEventDeliverable,
				types.EngineEventPlanProposed,
				types.EngineEventPlanApproved,
				types.EngineEventPlanCreated,
				types.EngineEventPlanUpdated,
				types.EngineEventPlanCompleted,
				types.EngineEventPlanFailed,
				types.EngineEventStepDispatched,
				types.EngineEventStepStarted,
				types.EngineEventStepProgress,
				types.EngineEventStepCompleted,
				types.EngineEventStepFailed,
				types.EngineEventStepSkipped,
				types.EngineEventStepDecisionRequested,
				types.EngineEventLLMHeartbeat,
				types.EngineEventLLMRetry,
				types.EngineEventSystemNotice:
				// Pass through unchanged — the deeper layer already stamped
				// the correct AgentID/AgentName, and re-wrapping would lose
				// that attribution. ParentAgentID stitches the chain back
				// together for the WebSocket client.
				//
				// Plan / step lifecycle events (added v1.16) carry the
				// per-plan task graph + per-step dispatch/result info the
				// client needs to render the plan tree before any sub-agent
				// events arrive; without this case they're silently dropped.
				//
				// step_decision_requested is the user-facing failure-gate
				// prompt emitted by Scheduler / PlanCoordinator inside an
				// L2 worker (PlanCoordinator runs there). Without this
				// pass-through the prompt never reaches the WebSocket and
				// the engine's requestStepDecision blocks forever.
				//
				// system_notice is emitted by SearchGapDetector when an L3
				// is spawned with declared search capability but no search
				// tool registered. The detector writes directly into
				// cfg.ParentOut, so when the L3 sits under an L2 dispatcher
				// the event must travel up two layers; without this case
				// the system card never reaches the WebSocket.
				cfg.ParentOut <- evt
			}
		}
	}
	<-done

	elapsed := time.Since(startTime)

	terminal := loopResult.Terminal

	// Step 13: Emit subagent.end event.
	agentStatus := "completed"
	switch terminal.Reason {
	case types.TerminalMaxTurns:
		agentStatus = "max_turns"
	case types.TerminalModelError:
		agentStatus = "model_error"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		agentStatus = "aborted"
	}
	// Prefer submit_task_result-validated artifacts when present — that's
	// the canonical "deliverables" set. Fall back to the broader
	// produced-while-running list (everything any tool wrote) when the
	// task had no contract.
	endArtifacts := producedArtifacts
	if len(loopResult.SubmittedArtifacts) > 0 {
		endArtifacts = loopResult.SubmittedArtifacts
	}
	if cfg.ParentOut != nil {
		cfg.ParentOut <- types.EngineEvent{
			Type:         types.EngineEventSubAgentEnd,
			AgentID:      agentID,
			AgentName:    cfg.Name,
			AgentStatus:  agentStatus,
			SubagentType: cfg.SubagentType, // mirror SubAgentStart so end-card has the worker label too
			Duration:     elapsed.Milliseconds(),
			Usage:        &cumulativeUsage,
			Terminal: &types.Terminal{
				Reason: terminal.Reason,
				Turn:   terminal.Turn,
			},
			// Doc §10 aggregated form — every artifact this sub-agent
			// produced, in tool-call order. The UI uses this to render a
			// single "outputs" card on the sub-agent panel without having
			// to replay the per-tool stream.
			Artifacts: endArtifacts,
		}
	}
	// Tracker hook: mark this sub-agent row finished with the same
	// status/duration the wire event carries. Symmetric with the
	// StartSubAgent call above; same nil-guards apply.
	if statsReg := s.deps.StatsRegistry(); statsReg != nil && cfg.ParentSessionID != "" {
		if tr := statsReg.Get(cfg.ParentSessionID); tr != nil {
			tr.FinishSubAgent(agentID, agentStatus, elapsed.Milliseconds())
		}
	}
	// Plan B dual-write: finish the root tracker row too when root differs
	// from immediate parent — mirrors the StartSubAgent dual-write above.
	if statsReg := s.deps.StatsRegistry(); statsReg != nil && rootSID != "" && rootSID != cfg.ParentSessionID {
		if tr := statsReg.Get(rootSID); tr != nil {
			tr.FinishSubAgent(agentID, agentStatus, elapsed.Milliseconds())
		}
	}
	if bus := s.deps.EventBus(); bus != nil {
		bus.Publish(event.Event{
			Topic: event.TopicSubAgentEnded,
			Payload: map[string]any{
				"agent_id":    agentID,
				"name":        cfg.Name,
				"reason":      string(terminal.Reason),
				"turns":       terminal.Turn,
				"duration_ms": elapsed.Milliseconds(),
			},
		})
	}

	// Per-terminal log level so monitoring's Error / Warn streams aren't
	// drowned by user-initiated cancellations:
	//   - Completed                              → Info  (happy path)
	//   - AbortedStreaming / AbortedTools        → Info  (external cancel,
	//                                                     not a failure)
	//   - MaxTurns                               → Warn  (soft cap hit;
	//                                                     surface for tuning)
	//   - ModelError / ContextExceeded / other   → Error (genuine failure)
	completionFields := []zap.Field{
		zap.String("terminal_reason", string(terminal.Reason)),
		zap.String("terminal_message", truncateForLog(terminal.Message, 200)),
		zap.Int("turns", terminal.Turn),
		zap.Duration("elapsed", elapsed),
		zap.Int("submitted_artifacts", len(loopResult.SubmittedArtifacts)),
		zap.Int("contract_failures", len(loopResult.ContractFailures)),
	}
	if len(loopResult.ContractFailures) > 0 {
		completionFields = append(completionFields,
			zap.Strings("failure_sample", contractFailureSample(loopResult.ContractFailures, 3)))
	}
	if loopResult.NeedsPlanning {
		completionFields = append(completionFields,
			zap.Bool("needs_planning", true),
			zap.String("escalation_reason", truncateForLog(loopResult.EscalationReason, 200)))
	}
	switch terminal.Reason {
	case types.TerminalCompleted:
		logger.Info("sub-agent completed", completionFields...)
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		logger.Info("sub-agent cancelled by external signal", completionFields...)
	case types.TerminalMaxTurns:
		logger.Warn("sub-agent hit max turns", completionFields...)
	default:
		logger.Error("sub-agent ended with failure terminal", completionFields...)
	}

	// Step 14: Return SpawnResult with structured fields.
	// - Output: full text (stored in TaskRegistry for reference)
	// - Summary: extracted from <summary> tag (returned to the spawning parent in tool_result)
	// - Status: derived from terminal reason
	fullOutput := textBuf.String()
	summary := parseSummaryTag(fullOutput)
	// Plan-mode coordinators populate loopResult.Summary directly (their
	// LLM-less ReviewGoal path doesn't go through the assistant text
	// channel). When set, prefer it over what parseSummaryTag found —
	// the explicitly-built summary is canonical.
	if loopResult.Summary != "" {
		summary = loopResult.Summary
	}

	// Derive status from terminal reason.
	status := "completed"
	switch terminal.Reason {
	case types.TerminalMaxTurns:
		status = "max_turns"
	case types.TerminalModelError:
		status = "error"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		status = "aborted"
	}

	// Build the output the spawning parent (typically the L1 main agent)
	// sees in tool_result. Three sections, top-down:
	//   1. <summary> — what the sub-agent reports it did
	//   2. 产出 artifact — IDs the parent can quote / ArtifactRead
	//   3. 产出文件 — FileWrite-side deliverables (legacy path)
	//
	// Why artifacts must surface here: the LLM only reads tool_result
	// content; subagent_end events go to the WebSocket client. Without
	// listing the IDs, emma has no way to reference produced artifacts
	// in her reply (e.g. "详情见 art_xxx") — she'd have to either
	// fabricate IDs or re-paste content. The `[role]` prefix lets emma
	// pick the right artifact when the contract had multiple roles.
	//
	// The full sub-agent transcript is preserved in TaskRegistry; this
	// view is intentionally narrow so emma's context stays tight.
	var parentVisibleOutput strings.Builder
	parentVisibleOutput.WriteString(summary)
	if len(deliverables) > 0 {
		parentVisibleOutput.WriteString("\n产出文件：\n")
		for _, d := range deliverables {
			parentVisibleOutput.WriteString(fmt.Sprintf("- %s（%s，%d 字节）\n", d.FilePath, d.Language, d.ByteSize))
		}
	}

	// L3-only signal: when the driver flipped NeedsPlanning, override
	// status so the parent's tool_result content reflects it. The parent
	// LLM reads "needs_planning" and re-plans rather than treating an
	// escalation like a normal completion.
	if loopResult.NeedsPlanning {
		status = "needs_planning"
	}

	spawnResult := &agent.SpawnResult{
		Output:       parentVisibleOutput.String(),
		Summary:      summary,
		Status:       status,
		Attempts:     1, // TODO: increment when task-level retry is implemented
		Deliverables: deliverables,
		Terminal:     &terminal,
		Usage:        &cumulativeUsage,
		SessionID:    sessionID,
		AgentID:      agentID,
		NumTurns:     terminal.Turn,
		// Surface contract validation outcome (doc §3 M3/M4): the parent
		// reads these to decide between "integrate" / "retry" / "abort".
		SubmittedArtifacts: loopResult.SubmittedArtifacts,
		ContractFailures:   loopResult.ContractFailures,
		// L3 escalation surface (TierSubAgent driver only). Zero-valued
		// for L2 coordinator runs, populated when the L3 called
		// escalate_to_planner instead of submit_task_result.
		NeedsPlanning:      loopResult.NeedsPlanning,
		EscalationReason:   loopResult.EscalationReason,
		SuggestedNextSteps: loopResult.SuggestedNextSteps,
		SelfCheckFailures:  loopResult.SelfCheckFailures,

		// L2 coordinator surface (Phase B+). Empty for L3 spawns.
		CoordinatorMode:   loopResult.CoordinatorMode,
		EscalatedFromMode: loopResult.EscalatedFromMode,
		BudgetSpent:       loopResult.BudgetSpent,
	}

	// Record full result in TaskRegistry for future reference (context passing, debugging).
	// Store the full output separately — the spawning parent only sees the summary.
	fullResult := *spawnResult
	fullResult.Output = fullOutput // preserve full sub-agent output
	s.taskRegistryMu.Lock()
	s.taskRegistry[agentID] = &fullResult
	s.taskRegistryMu.Unlock()

	// DEBUG: spawn.end data-flow snapshot — pair with `spawn.start` to
	// see one full sub-agent run. parent_visible_preview shows EXACTLY
	// what the dispatching tool will hand back to the parent LLM as
	// tool_result.Content (after BuildFailureContent or summary+artifacts
	// composition). This is the load-bearing field for "did emma see
	// the artifacts?" debugging.
	logger.Debug("spawn.end",
		zap.String("agent_id", agentID),
		zap.String("status", status),
		zap.String("terminal_reason", string(terminal.Reason)),
		zap.Int("turns", terminal.Turn),
		zap.Duration("elapsed", elapsed),
		zap.Int("parent_visible_len", len(spawnResult.Output)),
		zap.String("parent_visible_preview", truncateForLog(spawnResult.Output, 400)),
		zap.Int("submitted_artifacts", len(spawnResult.SubmittedArtifacts)),
		zap.Strings("submitted_ids", refIDs(spawnResult.SubmittedArtifacts)),
		zap.Int("deliverables", len(spawnResult.Deliverables)),
		zap.Int("contract_failures", len(spawnResult.ContractFailures)),
	)

	return spawnResult, nil
}

// loopConfig parameterizes the query loop for both main and sub-agent execution.
type loopConfig struct {
	pool                 *tool.ToolPool
	profile              *prompt.AgentProfile
	permChecker          permission.Checker
	config               SpawnConfig
	systemPromptOverride string
	subagentType         string          // agent definition name (e.g., "developer", "researcher")
	allowedSkills        map[string]bool // nil = all skills; non-nil = whitelist
	logger               *zap.Logger
	// agentID is the spawned sub-agent's identifier. Stamped onto every
	// artifact this loop writes so lineage (doc §4 producer.agent_id) is
	// preserved across spawn.
	agentID string
	// taskID is the orchestrator's identifier for this work unit. Empty
	// when no contract was supplied (legacy / simple-task path).
	taskID string
	// taskStartedAt anchors the temporal "no time travel" check applied
	// by submit_task_result. Zero disables that check.
	taskStartedAt time.Time
	// expectedOutputs is the deliverable contract supplied by the parent.
	// Length 0 = no contract; loop terminates on end_turn as before.
	// Length > 0 = submit_task_result required; loop refuses to terminate
	// without a passing submit (M3 + M4 from doc §3).
	expectedOutputs []types.ExpectedOutput
	// temperature overrides the LLM sampling temperature for this loop.
	// Nil leaves the request's Temperature at its zero value (provider
	// uses its own default). Sourced from AgentDefinition.Temperature
	// for sub-agents, nil for the legacy main-agent loop.
	temperature *float64
	// outputSchema is the per-agent declared structured output shape
	// (AgentDefinition.OutputSchema). When set the submit_task_result
	// server-side validation matches submitted `result` against it.
	outputSchema map[string]any
	// originalPrompt is the natural-language task seed the dispatcher
	// supplied (cfg.Prompt). The query loop already injects this as the
	// first user message; PlanCoordinator reads it back here to feed
	// the Planner without re-deriving from session state.
	originalPrompt string
	// skillTracker is non-nil only when this loop is running the freelancer
	// AgentDefinition. The four skill self-management tools (load_skill /
	// unload_skill / list_loaded_skills) pick it up via ctx. nil = not a
	// freelancer spawn; those tools refuse to run.
	skillTracker *loop.SkillTracker
	// readScope / writeScope / sessionRoot are the per-spawn filesystem
	// scope plumbed onto every tool ctx via ToolExecutor.SetAgentScope.
	// All empty means no restriction (legacy compat path).
	readScope   []string
	writeScope  []string
	sessionRoot string
	// stripDispatchTools instructs runSubAgentDriver to strip dispatchToolNames
	// from the pool before the LLM loop. True for strict leaf agents (TierSubAgent)
	// that must never call back into dispatch. False for RunAsLLMAgent agents
	// (e.g. plan-executor-agent) whose AllowedTools whitelist already controls
	// which dispatch tools are present in the pool.
	stripDispatchTools bool
}

// buildSubmitNudgeMessage assembles the SYSTEM-style reminder injected
// when an L3 reaches end_turn without a passing submit_task_result.
// Listed by [SYSTEM] tag so the LLM treats it as framework directive,
// not a user input. Includes the contract roles so the model has
// everything it needs to make progress.
func buildSubmitNudgeMessage(nudge int, outs []types.ExpectedOutput) types.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "[SYSTEM] 你尚未调用 submit_task_result 提交产物。任务未完成，请立即调用 (提示 %d/%d)。\n",
		nudge, maxSubmitNudges)
	if nudge >= 2 {
		b.WriteString("强制提示：先用 write 把产物写到 {task_dir}/ 下 → meta_write 登记 outputs[] → submit_task_result({task_id, meta_path})。\n")
	}
	if nudge >= maxSubmitNudges {
		b.WriteString("这是最后一次机会，再不提交将判定任务失败。\n")
	}
	if len(outs) > 0 {
		b.WriteString("\n本任务必交 role 列表：\n")
		for _, o := range outs {
			if o.Required {
				fmt.Fprintf(&b, "- %s\n", o.Role)
			}
		}
	}
	return types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText,
			Text: b.String(),
		}},
		CreatedAt: time.Now(),
	}
}

// buildSubAgentSystemPrompt builds a system prompt for the sub-agent using
// the given profile, without touching the parent's prompt cache.
// subagentType is the agent definition name (e.g., "developer", "researcher")
// used to look up the worker's identity from the definition registry.
// When allowedSkills is non-nil, only the listed skills appear in the prompt.
// pool is the filtered ToolPool whose schemas the LLM actually sees — passed
// in so the rendered "# 可用工具" block matches the callable set rather than
// the global registry.
// BuildSubAgentSystemPrompt is the test-friendly export of the
// internal prompt assembler. Production callers use the unexported
// path inside runSubAgentDriver.
func (s *Spawner) BuildSubAgentSystemPrompt(
	ctx context.Context,
	sess *session.Session,
	messages []types.Message,
	profile *prompt.AgentProfile,
	subagentType string,
	allowedSkills map[string]bool,
	pool *tool.ToolPool,
	sessionRoot string,
) string {
	return s.buildSubAgentSystemPrompt(ctx, sess, messages, profile, subagentType, allowedSkills, pool, sessionRoot)
}

func (s *Spawner) buildSubAgentSystemPrompt(
	_ context.Context,
	sess *session.Session,
	messages []types.Message,
	profile *prompt.AgentProfile,
	subagentType string,
	allowedSkills map[string]bool,
	pool *tool.ToolPool,
	sessionRoot string,
) string {
	cfg := s.deps.SpawnerConfig()
	pb := s.deps.PromptBuilder()
	logger := s.deps.Logger()
	if pb == nil {
		return cfg.SystemPrompt
	}

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += msg.Tokens
	}

	// Build skill listing, filtering by allowedSkills if set.
	skillListing := s.deps.GetSkillListingFiltered(allowedSkills)

	// Look up agent definition to build worker identity, tool filter, and skills.
	//
	// workerIdentity becomes PromptContext.SystemPromptOverride, which
	// (per builder.go) takes precedence over the profile's static
	// SectionOverrides["role"]. The decision tree:
	//
	//   1. def.SystemPrompt set                    → honour it (explicit author intent)
	//   2. def.IsTeamMember=true                   → BuildWorkerIdentity (personalised "你叫小林…")
	//   3. profile has no role SectionOverride     → BuildWorkerIdentity (otherwise the
	//                                                role section falls through to
	//                                                IdentitySection which returns the
	//                                                L1 emma persona — leaking emma's
	//                                                identity into L3 prompts)
	//   4. profile has role SectionOverride        → leave empty so the profile's
	//                                                static role text wins
	//                                                (scheduler / Explore / Plan)
	//
	// Without case 3, dispatching `general-purpose` (IsTeamMember=false,
	// WorkerProfile has no role override) would silently install emma's
	// IdentitySection content into the L3 role section.
	var workerIdentity string
	var def *agent.AgentDefinition
	if defReg := s.deps.DefRegistry(); defReg != nil && subagentType != "" {
		def = defReg.Get(subagentType)
	}
	if def != nil {
		profileHasRoleOverride := profile != nil &&
			profile.SectionOverrides != nil &&
			profile.SectionOverrides["role"] != ""

		// Leaf isolation (your L3 design point #1):
		//   "L3 sub-agent 不知道 Emma 是谁"
		// For TierSubAgent we deliberately drop the leader name — the
		// identity reads "你叫小林，是团队的搭档" instead of "你叫小林，是
		// emma 团队的搭档". For coordinators (scheduler / Plan / etc.)
		// we still surface the leader because they DO need to coordinate
		// with the user-facing agent.
		leaderName := cfg.MainAgentDisplayName
		if def.EffectiveTier() == agent.TierSubAgent {
			leaderName = ""
		}
		switch {
		case def.SystemPrompt != "":
			workerIdentity = def.SystemPrompt
		case def.EffectiveTier() == agent.TierSubAgent:
			// L3 sub-agents are pure functional — no team affiliation, no
			// personality injection. BuildFunctionalIdentity generates a
			// task-focused identity that doesn't reference emma or the team.
			workerIdentity = texts.BuildFunctionalIdentity(
				def.DisplayName,
				def.Description,
			)
		case def.IsTeamMember:
			workerIdentity = texts.BuildWorkerIdentity(
				def.DisplayName,
				leaderName,
				def.Description,
				def.Personality,
			)
		case !profileHasRoleOverride:
			workerIdentity = texts.BuildWorkerIdentity(
				def.DisplayName,
				leaderName,
				def.Description,
				def.Personality,
			)
		}
	}

	// Inject the filtered tool set so ToolsSection renders only the tools
	// the LLM can actually call. Without this the prompt would list the
	// entire global registry while the schema list is restricted by
	// AgentDefinition.AllowedTools / AgentType blacklist — a mismatch that
	// wastes tokens and tempts the model into doomed tool calls.
	var availableTools []tool.Tool
	if pool != nil {
		availableTools = pool.All()
	}

	promptCtx := &prompt.PromptContext{
		SessionID:            sess.ID,
		Turn:                 len(messages),
		Session:              sess,
		Tools:                s.deps.Registry(),
		AvailableTools:       availableTools,
		TotalTokensUsed:      totalTokens,
		ContextWindowSize:    s.deps.ContextWindow(),
		Memory:               make(map[string]string),
		EnvInfo:              s.deps.GetEnvSnapshot(sessionRoot),
		SkillListing:         skillListing,
		SystemPromptOverride: workerIdentity,
	}

	output, err := pb.Build(promptCtx, profile)
	if err != nil {
		logger.Warn("sub-agent prompt build failed, using fallback", zap.Error(err))
		return cfg.SystemPrompt
	}

	result := output.ToSystemPrompt()
	logger.Debug("========== SUB-AGENT SYSTEM PROMPT START ==========\n"+result+"\n========== SUB-AGENT SYSTEM PROMPT END ==========",
		zap.String("session_id", sess.ID),
		zap.String("profile", profile.Name),
		zap.Int("char_count", len(result)),
		zap.Int("estimated_tokens", prompt.EstimateTokens(result)),
		zap.Int("block_count", len(output.Blocks)),
	)

	return result
}

// ResolveSubAgentProfile maps a subagent_type string to a prompt profile.
// Exported so tests in the engine package can verify the mapping without
// reaching into spawn-private state.
func ResolveSubAgentProfile(subagentType string) *prompt.AgentProfile {
	return prompt.ResolveProfileBySubagentType(subagentType)
}

// resolveSubAgentProfile is the in-package alias kept for the existing
// call site in SpawnSync. New callers should use the exported name.
func resolveSubAgentProfile(subagentType string) *prompt.AgentProfile {
	return ResolveSubAgentProfile(subagentType)
}

// subAgentLoopResult bundles the loop's terminal state with the
// contract-validation outcome (doc §3 mechanism M3/M4). SpawnSync reads
// it to populate SpawnResult.SubmittedArtifacts / ContractFailures so the
// parent agent gets a structured view of "what landed" without parsing
// streamed events.
//
// NeedsPlanning / EscalationReason / SuggestedNextSteps are populated
// only by runSubAgentDriver when escalate_to_planner fired.
type subAgentLoopResult struct {
	Terminal           types.Terminal
	SubmittedArtifacts []types.ArtifactRef
	ContractFailures   []string

	// Populated by runSubAgentDriver on escalate_to_planner; zero otherwise.
	NeedsPlanning      bool
	EscalationReason   string
	SuggestedNextSteps string
	SelfCheckFailures  []string

	// L2-coordinator fields. Populated when the loop ran a Coordinator
	// (ReAct / Plan / future). Empty for L3 / legacy direct-loop spawns.
	//
	// CoordinatorMode is the FINAL mode that produced the result. If
	// ReAct auto-escalated to Plan, this is "plan".
	// EscalatedFromMode is non-empty only when an internal mode promotion
	// happened — e.g., D-mode escalation logs "react" → "plan" so emma
	// can explain "我们一开始用的快路径，发现没把握就升级了 plan 模式".
	CoordinatorMode   string
	EscalatedFromMode string
	BudgetSpent       agent.BudgetSpent

	// Summary is the coordinator-level <summary> the parent agent (emma)
	// quotes when speaking to the user. Plan mode populates this; ReAct
	// leaves it empty because the L2 LLM already produces its own
	// <summary> via the assistant text path.
	Summary string
}

// maxSubmitNudges caps how many times the loop will re-prompt an L3 that
// reaches end_turn without a passing submit_task_result. Doc §7 — three
// strikes covers honest mistakes (forgot, mis-roled, missing required)
// without giving an adversarial / broken model unlimited turns.
const maxSubmitNudges = 3

// maxSubmitRejects caps how many failed validations (M4) we accept
// before declaring the task lost. Distinct from nudges: a nudge happens
// when the LLM didn't call submit; a reject when it called submit but
// the artifacts didn't pass. Both share a cap of 3 — same logic, same
// philosophy.
const maxSubmitRejects = 3

// joinNonEmpty stitches non-empty strings with sep. Used to compose the
// task user message from preamble blocks where any block may be absent.
// strings.Join would emit duplicate separators when middle entries are "".
func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// metaRefToLoopResult converts a successful SchedulerCoordinator result
// into a subAgentLoopResult. Only the Terminal field is populated;
// the caller fills CoordinatorMode.
func metaRefToLoopResult(ref schedulertypes.MetaRef, rootDir, sessionID string) subAgentLoopResult {
	res := subAgentLoopResult{
		Terminal: types.Terminal{Reason: types.TerminalCompleted},
	}
	// Read the meta.json written by the leaf runner to surface the summary
	// and any outputs back to the L1 caller. Without this the parent sees
	// an empty response.
	if rootDir != "" && sessionID != "" && string(ref) != "" {
		absPath := filepath.Join(workspace.SessionRoot(rootDir, sessionID), string(ref))
		if b, err := os.ReadFile(absPath); err == nil {
			var m workspace.Meta
			if json.Unmarshal(b, &m) == nil {
				var sb strings.Builder
				sb.WriteString(m.Summary)
				if len(m.Outputs) > 0 {
					sb.WriteString("\n产出文件：\n")
					for _, o := range m.Outputs {
						if o.Path != "" {
							sb.WriteString("- ")
							sb.WriteString(o.Path)
							sb.WriteString("\n")
						}
					}
				}
				// Surface the deliverables directory so L1 (emma) can present
				// a single stable path to the user instead of per-task paths.
				delivDir := workspace.DeliverablesDir(rootDir, sessionID)
				if entries, err := os.ReadDir(delivDir); err == nil && len(entries) > 0 {
					sb.WriteString("\n交付目录（已整理至此）：")
					sb.WriteString(delivDir)
					sb.WriteString("\n")
				}
				res.Summary = sb.String()
			}
		}
	}
	return res
}

// countRequired returns how many ExpectedOutputs are marked Required.
// Used by the spawn.start debug log to preview the contract — operators
// can see at a glance "this dispatch demands 2 mandatory outputs".
func countRequired(outs []types.ExpectedOutput) int {
	n := 0
	for _, o := range outs {
		if o.Required {
			n++
		}
	}
	return n
}

// refIDs extracts just the artifact_ids from a Refs slice, suitable for
// dumping into a single zap.Strings field. Keeps the spawn.end log line
// compact while still letting operators trace artifacts across logs.
func refIDs(refs []types.ArtifactRef) []string {
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ArtifactID)
	}
	return ids
}

// composeTaskInputsPreamble renders the <task-inputs> block listing the
// upstream task output files this spawn will read. Each entry pulls its
// summary from the dep's meta.json (sibling file under .../tasks/{tid}/),
// so the L3 sees not just paths but a one-line description of each.
//
// Empty cfg.InputPaths returns "" — the legacy artifact-store path also
// returned "" in the common single-task case, so callers compose
// unconditionally without ever wrapping the result.
func composeTaskInputsPreamble(cfg *agent.SpawnConfig, logger *zap.Logger) string {
	if len(cfg.InputPaths) == 0 {
		return ""
	}
	root := workspaceRootDir()
	rootSID := cfg.RootSessionID
	if rootSID == "" {
		rootSID = cfg.ParentSessionID
	}
	sessionRoot := ""
	if root != "" && rootSID != "" {
		sessionRoot = workspace.SessionRoot(root, rootSID)
	}
	refs := make([]workspace.TaskInputRef, 0, len(cfg.InputPaths))
	for _, p := range cfg.InputPaths {
		ref := workspace.TaskInputRef{Path: p}
		if sessionRoot != "" {
			if rel, err := filepath.Rel(sessionRoot, p); err == nil && !strings.HasPrefix(rel, "..") {
				ref.Path = rel
			}
		}
		if fi, err := os.Stat(p); err == nil {
			ref.Bytes = int(fi.Size())
		}
		if root != "" && rootSID != "" {
			if depID := guessTaskIDFromPath(p); depID != "" {
				if b, err := os.ReadFile(workspace.MetaPath(root, rootSID, depID)); err == nil {
					var m workspace.Meta
					if json.Unmarshal(b, &m) == nil {
						ref.Summary = m.Summary
					}
				}
			}
		}
		refs = append(refs, ref)
	}
	out := workspace.RenderTaskInputs(refs)
	if out != "" {
		logger.Info("task-inputs preamble injected",
			zap.Int("count", len(refs)),
			zap.String("root_sid", rootSID),
		)
	}
	return out
}

// guessTaskIDFromPath extracts the upstream task id from a path that looks
// like .../tasks/{tid}/<file>. Empty when the path doesn't carry that
// shape — the preamble degrades to "path only" instead of failing.
func guessTaskIDFromPath(p string) string {
	parts := strings.Split(filepath.Clean(p), string(filepath.Separator))
	for i, e := range parts {
		if e == "tasks" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// truncateRunes truncates s to at most n runes (NOT bytes), appending an
// ellipsis when truncation actually happened. Used for wire payloads
// where the source may be Chinese or other multibyte text — byte-level
// truncation would split a codepoint and produce \xe5 garbage.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 4 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// summaryTagRe matches <summary>...</summary> in sub-agent output.
// Uses (?s) so . matches newlines within the tag.
var summaryTagRe = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

// parseSummaryTag extracts the content of the first <summary> tag from text.
// Fallback: returns the first non-empty paragraph, truncated to 200 chars.
func parseSummaryTag(text string) string {
	if m := summaryTagRe.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}

	// Fallback: first non-empty paragraph.
	for _, para := range strings.Split(text, "\n\n") {
		p := strings.TrimSpace(para)
		if p != "" {
			if len([]rune(p)) > 200 {
				return string([]rune(p)[:200]) + "..."
			}
			return p
		}
	}
	return ""
}

// parseCandidateSkills extracts the candidate_skills array from
// SpawnConfig.Inputs. Returns empty slice for nil / wrong-type input.
// Strings only — non-string elements are skipped (defensive against
// loose JSON unmarshalling).
// defHasSkillSelfMgmtTool reports whether an agent definition's AllowedTools
// includes any of the skill self-management tools (search_skill / load_skill /
// unload_skill / list_loaded_skills). When true, SpawnSync creates a SkillTracker
// so those tools have somewhere to store state — regardless of whether the
// agent is `freelancer` or one of the fixed L3 partners that's been enhanced
// with skill access.
func defHasSkillSelfMgmtTool(allowed []string) bool {
	for _, t := range allowed {
		switch t {
		case "search_skill", "load_skill", "unload_skill", "list_loaded_skills":
			return true
		}
	}
	return false
}

func parseCandidateSkills(inputs map[string]any) []string {
	if inputs == nil {
		return nil
	}
	raw, ok := inputs["candidate_skills"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hydrateFreelancer prepares a SkillTracker + augmented prompt for the
// freelancer agent. Returns (tracker, newPrompt, err).
//
// When candidates is empty, returns a fresh empty tracker and the prompt
// unchanged — freelancer can still search_skill at runtime.
//
// On error (missing skill, too many candidates) returns (nil, "", err) so
// SpawnSync can fail fast before any LLM call.
func hydrateFreelancer(reader *skill.Reader, buildLoadedSkillsBlock func(fulls []*skill.SkillFull) string, candidates []string, prompt string) (*loop.SkillTracker, string, error) {
	tracker := loop.NewSkillTracker(3)

	if len(candidates) == 0 {
		return tracker, prompt, nil
	}
	if len(candidates) > 3 {
		return nil, "", fmt.Errorf("candidate_skills limit is 3, got %d", len(candidates))
	}
	if reader == nil {
		return nil, "", fmt.Errorf("skill reader not configured; cannot resolve candidate_skills")
	}

	fulls := make([]*skill.SkillFull, 0, len(candidates))
	for _, name := range candidates {
		full, err := reader.Load(name)
		if err != nil {
			return nil, "", fmt.Errorf("candidate skill %q: %w", name, err)
		}
		fulls = append(fulls, full)
	}
	if err := tracker.Preload(fulls); err != nil {
		return nil, "", err
	}
	block := buildLoadedSkillsBlock(fulls)
	newPrompt := block + "\n\n" + prompt
	return tracker, newPrompt, nil
}

// truncateForLog clips s to at most n bytes (rune-safe) for log output.
// Local copy of the engine package's helper so spawn doesn't need to
// import engine. Keep behaviour identical — tests assert byte-equal
// outputs.
func truncateForLog(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	// Drop trailing partial rune if we sliced mid-codepoint.
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + "...[truncated]"
}

// contractFailureSample returns up to n contract-failure strings, each
// rune-safely capped at 120 chars. Local copy of the engine helper.
func contractFailureSample(failures []string, n int) []string {
	limit := n
	if len(failures) < limit {
		limit = len(failures)
	}
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = truncateForLog(failures[i], 120)
	}
	return out
}
