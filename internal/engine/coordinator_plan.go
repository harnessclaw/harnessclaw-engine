package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// PlanCoordinator realises CoordinatorModePlan: plan-driven L2 coordination.
// Flow:
//  1. Planner produces a step DAG (HeuristicPlanner by default; an LLM
//     planner can be slotted in via SharedDeps.Planner)
//  2. Judge L1 validates the plan structurally
//  3. Scheduler executes steps sequentially, respecting BudgetTracker and
//     per-step Judge verdicts
//  4. Judge.ReviewGoal checks the aggregate output
//  5. On miss + budget remaining → re-plan with EscalationContext
//     On miss + no budget → FallbackChain aggregates partial outputs
//
// Up to maxPlanReplans re-plans are allowed before falling back; this
// caps the worst case at "Planner cost × N + execution cost".
type PlanCoordinator struct {
	deps *SharedDeps

	// escalation, when non-nil, is the carry-over from a predecessor
	// (ReAct or a previous Plan attempt). Planner uses it to skip
	// already-completed work.
	escalation *EscalationContext
}

// defaultMaxPlanReplans is the fallback when EngineConfig.MaxPlanReplans
// is zero (tests / legacy callers). Production reads the config-driven
// value through PlanCoordinator.maxReplans().
const defaultMaxPlanReplans = 3

// Mode reports CoordinatorModePlan.
func (c *PlanCoordinator) Mode() CoordinatorMode { return CoordinatorModePlan }

// maxReplans honours an EngineConfig override when wired and falls back
// to defaultMaxPlanReplans for tests / legacy callers.
func (c *PlanCoordinator) maxReplans() int {
	if c != nil && c.deps != nil && c.deps.QE != nil && c.deps.QE.config.MaxPlanReplans > 0 {
		return c.deps.QE.config.MaxPlanReplans
	}
	return defaultMaxPlanReplans
}

// WithEscalation returns a copy of c carrying ec as the seed escalation
// context. Used by the ReAct → Plan promotion path so a Plan run that
// inherits prior partial work can hand it to the Planner.
func (c *PlanCoordinator) WithEscalation(ec *EscalationContext) *PlanCoordinator {
	cp := *c
	cp.escalation = ec
	return &cp
}

// Run drives one Plan-mode execution. Returns a subAgentLoopResult shaped
// the same way as runSubAgentLoop / runSubAgentDriver so the wrapping
// SpawnSync goroutine doesn't need to special-case Plan mode.
//
// The implementation deliberately keeps the loop linear (not goroutine-
// based) so each phase's logging is sequential and easier to reason about.
// Parallel step dispatch is a follow-up that lives in the Scheduler, not
// the Coordinator.
func (c *PlanCoordinator) Run(
	ctx context.Context,
	sess *session.Session,
	lc *loopConfig,
	out chan<- types.EngineEvent,
) subAgentLoopResult {
	logger := c.deps.Logger.Named("plan-coord")
	goal := extractGoal(lc)
	available := availableSubagentsForPlanner(c.deps)

	// Plumb wire-routing once for the whole plan lifecycle. Both the
	// planner (called below) and the LLMSubagentResolver (called inside
	// scheduler.Run when a step needs subagent picking) live outside
	// the standard sub-agent driver loop that normally supplies these
	// fields directly. Without this, their heartbeats / retry-status
	// events would have nowhere to land — the L2 scheduler card
	// would sit silent during planning and per-step resolver calls.
	ctx = WithRetryRouting(ctx, lc.agentID, out)
	logger.Info("plan coordinator started",
		zap.String("goal_preview", truncForLog(goal, 120)),
		zap.Int("available_skills", len(available)),
		zap.Strings("skills", available),
		zap.String("escalation", c.escalation.FormatForLog()),
	)

	if len(available) == 0 {
		// Defensive guard: without skills the Planner is guaranteed to
		// fail. Surface this as an explicit fallback rather than letting
		// it cascade through Planner → re-plan loop. Common cause is
		// SQLite-loaded definitions missing the Tier field; fix the
		// registry hydration if this fires in production.
		logger.Error("plan coordinator: no L3 skills available; falling back",
			zap.String("hint", "agent definitions loaded from SQLite may be missing Tier=sub_agent"),
		)
		return c.fallbackResult(nil, nil,
			"plan mode unavailable: no L3 sub-agent skills registered (check AgentDefinition.Tier)",
			lc.agentID)
	}

	scheduler := NewScheduler(c.deps, nil, logger)

	// Build the parent SpawnConfig that step dispatches inherit from
	// (session lineage, parent event channel, etc.).
	rootSID, _ := sessionstats.RootSessionIDFromCtx(ctx)
	if rootSID == "" {
		// Defensive: if no root in ctx, treat sess.ID's parent (from session metadata)
		// as best-effort root. In practice ctx always carries root after SpawnSync.
		rootSID = sess.ID
	}
	parentCfg := &agent.SpawnConfig{
		ParentSessionID: sess.ID,
		RootSessionID:   rootSID,
		ParentOut:       out,
	}

	var (
		plan      *Plan
		outcome   *SchedulerOutcome
		lastError string
	)

	maxReplans := c.maxReplans()
	for replan := 0; replan <= maxReplans; replan++ {
		if exceeded, why := c.budgetExceeded(); exceeded {
			lastError = why
			break
		}

		// Plan (or re-plan).
		plannerInput := PlannerInput{
			Goal:            goal,
			Description:     lc.subagentType,
			AvailableSubagents: available,
			Escalation:      c.replanEscalation(replan, outcome, lastError),
		}
		plannerOut, err := c.deps.Planner.Plan(ctx, plannerInput)
		if err != nil {
			logger.Error("plan coordinator: Planner failed",
				zap.Int("replan", replan),
				zap.Error(err),
			)
			lastError = "planner: " + err.Error()
			break
		}
		plan = plannerOut.Plan
		logger.Info("plan produced",
			zap.Int("steps", len(plan.Steps)),
			zap.String("rationale", plannerOut.Rationale),
			zap.Int("replan", replan),
		)

		if v := c.deps.Judge.ReviewPlan(plan); !v.Pass {
			logger.Warn("plan coordinator: judge rejected plan",
				zap.Strings("reasons", v.Reasons),
				zap.Int("replan", replan),
			)
			lastError = "judge: " + strings.Join(v.Reasons, "; ")
			continue // try again
		}

		// Plan confirmation gate. When enabled (PlanConfirmation =
		// "required"), pause and emit prompt.user(plan_review) → block
		// on prompt.user_response. Treated identically to ask_user_question:
		// no card.add, no orphan watchdog, ctx deadline stripped — the
		// user gets all the time they need. Always-on cases (auto / empty)
		// skip the round-trip and run the plan as-is.
		//
		// Only the FIRST plan is gated; re-plans (replan > 0) are
		// programmatic recovery and don't bother the user again.
		// The user already approved the goal; if the execution fell
		// short, the coordinator's auto re-plan handles it.
		if replan == 0 && needsPlanConfirmation(ctx) {
			confirmed, cancelled, err := c.requestPlanConfirmation(ctx, lc, sess, out, plan, available, plannerOut.Rationale)
			if err != nil {
				logger.Error("plan coordinator: confirmation flow failed",
					zap.Error(err),
				)
				lastError = "confirmation: " + err.Error()
				break
			}
			if cancelled {
				logger.Info("plan coordinator: user rejected plan; cancelling")
				lastError = "user rejected plan"
				break
			}
			plan = confirmed // may be edited by user
		}

		// Emit plan_created (first iteration) or plan_updated (re-plans)
		// so the client opens the plan card and renders the step DAG.
		// Deliberately fired AFTER confirmation: during the confirm wait
		// no card exists, mirroring ask_user_question's prompt.user(question)
		// path which never opens a card while waiting on the user. This
		// is what keeps the lifecycle watchdog (CardPlan OrphanTimeoutMs
		// = 10min) from killing a plan that's just sitting in the user's
		// review queue.
		emitPlanLifecycle(out, plan, lc.agentID, replan)

		// Execute.
		outcome = scheduler.Run(ctx, plan, parentCfg)
		logger.Info("plan executed",
			zap.String("status", outcome.Status),
			zap.String("reason", outcome.Reason),
			zap.Int("step_count", len(outcome.StepResults)),
		)

		if outcome.Status == "success" {
			if v := c.deps.Judge.ReviewGoal(ctx, plan.Goal, outcome.StepResults); v.Pass {
				emitPlanCompleted(out, plan, lc.agentID)
				res := c.successResult(plan, outcome, lc.agentID)
				emitPlanSummaryText(out, res.Summary)
				return res
			} else {
				logger.Info("plan coordinator: review_goal failed; will replan if budget allows",
					zap.Strings("reasons", v.Reasons),
				)
				lastError = "review_goal: " + strings.Join(v.Reasons, "; ")
				continue
			}
		}
		// Scheduler returned status="cancelled" — user already decided
		// at a step gate; honour that decision and stop without asking
		// again at the plan level.
		if outcome != nil && outcome.Status == "cancelled" {
			lastError = outcome.Reason
			break
		}
		// status partial / failed — same path: re-plan if budget remains.
		lastError = outcome.Reason
		if outcome.Reason == "" {
			lastError = "plan execution did not reach success"
		}
	}

	// Exhausted re-plans, hit budget, or planner errored. Before
	// silently degrading to the fallback chain, ask the user. The user
	// has already paid for the work that ran; they should pick whether
	// to accept whatever partial result exists or cancel the run.
	// Skipped when the user already cancelled at a step gate.
	if !(outcome != nil && outcome.Status == "cancelled") {
		decision := c.askPlanDecisionOnFailure(ctx, sess, out, lc, lastError)
		if decision == types.StepDecisionCancel {
			logger.Info("plan coordinator: user cancelled after plan failure")
			emitPlanFailed(out, plan, lc.agentID, "user cancelled: "+lastError)
			res := c.fallbackResult(plan, outcome, "user cancelled: "+lastError, lc.agentID)
			emitPlanSummaryText(out, res.Summary)
			return res
		}
		// types.StepDecisionContinue (or unsupported retry) falls through
		// to the existing fallback aggregation.
	}

	logger.Error("plan coordinator: falling back",
		zap.String("last_error", lastError),
	)
	emitPlanFailed(out, plan, lc.agentID, lastError)
	res := c.fallbackResult(plan, outcome, lastError, lc.agentID)
	emitPlanSummaryText(out, res.Summary)
	return res
}

// emitPlanSummaryText pushes the assembled <summary> as a text event so
// SpawnSync's parentVisibleOutput aggregator captures it into
// SpawnResult.Output. Called once at the very end of a Plan run; ReAct
// produces its own summary via the LLM text path.
func emitPlanSummaryText(out chan<- types.EngineEvent, summary string) {
	if out == nil || summary == "" {
		return
	}
	out <- types.EngineEvent{Type: types.EngineEventText, Text: summary}
}

// emitPlanLifecycle pushes plan_created (first iteration) or
// plan_updated (re-plans) carrying the full step DAG. Front-ends use
// these events to render the plan tree before any step dispatches —
// without them the UI only sees per-step events with no top-level
// container to attach them to.
//
// SubagentType is left blank in PlanTaskInfo because the executor is
// resolved at dispatch time (v1.16+); step_dispatched will carry the
// chosen sub-agent.
func emitPlanLifecycle(out chan<- types.EngineEvent, plan *Plan, agentID string, iteration int) {
	if out == nil || plan == nil {
		return
	}
	tasks := make([]types.PlanTaskInfo, len(plan.Steps))
	for i, s := range plan.Steps {
		tasks[i] = types.PlanTaskInfo{
			TaskID:          s.ID,
			SubagentType:    s.SubagentType, // empty when resolver picks later
			DependsOn:       append([]string(nil), s.DependsOn...),
			UserFacingTitle: s.Description,
		}
	}
	evtType := types.EngineEventPlanCreated
	status := "created"
	if iteration > 0 {
		evtType = types.EngineEventPlanUpdated
		status = "updated"
	}
	out <- types.EngineEvent{
		Type:    evtType,
		AgentID: agentID,
		PlanEvent: &types.PlanEvent{
			PlanID:   "plan_" + agentID, // 1:1 with the coordinator instance for now
			Goal:     plan.Goal,
			Strategy: "sequential",
			Status:   status,
			Tasks:    tasks,
		},
	}
}

// emitPlanCompleted fires once when ReviewGoal passes — the canonical
// "plan finished cleanly" signal.
func emitPlanCompleted(out chan<- types.EngineEvent, plan *Plan, agentID string) {
	if out == nil || plan == nil {
		return
	}
	out <- types.EngineEvent{
		Type:    types.EngineEventPlanCompleted,
		AgentID: agentID,
		PlanEvent: &types.PlanEvent{
			PlanID: "plan_" + agentID,
			Goal:   plan.Goal,
			Status: "completed",
		},
	}
}

// emitPlanFailed fires when the coordinator gives up (re-plans
// exhausted / budget exhausted / user rejected). reason is surfaced to
// the client so a degraded run still has an explanation.
func emitPlanFailed(out chan<- types.EngineEvent, plan *Plan, agentID string, reason string) {
	if out == nil {
		return
	}
	pe := &types.PlanEvent{
		PlanID: "plan_" + agentID,
		Status: "failed",
	}
	if plan != nil {
		pe.Goal = plan.Goal
	}
	out <- types.EngineEvent{
		Type:    types.EngineEventPlanFailed,
		AgentID: agentID,
		PlanEvent: pe,
		TaskDispatch: &types.TaskDispatch{
			TaskID: pe.PlanID,
			Reason: reason,
		},
	}
}

// needsPlanConfirmation returns true when the caller asked the
// coordinator to pause for user review. Default is "auto" (false) — only
// "required" enables the round-trip.
func needsPlanConfirmation(ctx context.Context) bool {
	return tool.GetPlanConfirmation(ctx) == "required"
}

// requestPlanConfirmation runs the plan.proposed → plan.response cycle.
// Returns:
//   - finalPlan: the plan to execute (original or user-edited)
//   - cancelled: true when the user explicitly rejected (graceful abort)
//   - err: ctx cancellation, validation failure on edited plan, etc.
//
// Edited plans go through Plan.Validate before being accepted. A
// validation-failed edit is treated as cancellation — the coordinator
// surfaces it via the fallback path; we don't loop trying to re-confirm
// the same broken edit.
func (c *PlanCoordinator) requestPlanConfirmation(
	ctx context.Context,
	lc *loopConfig,
	sess *session.Session,
	out chan<- types.EngineEvent,
	plan *Plan,
	availableSubagents []string,
	rationale string,
) (*Plan, bool, error) {
	if c.deps == nil || c.deps.QE == nil {
		// No engine handle — can't request approval. Treat as auto.
		return plan, false, nil
	}

	proposal := &types.PlanProposal{
		PlanID:          newPlanID(),
		AgentID:         lc.agentID,
		Goal:            plan.Goal,
		Rationale:       rationale,
		Steps:           toProposedSteps(plan.Steps),
		AvailableSubagents: append([]string(nil), availableSubagents...),
	}

	c.deps.Logger.Info("plan coordinator: awaiting user confirmation",
		zap.String("plan_id", proposal.PlanID),
		zap.Int("steps", len(proposal.Steps)),
	)

	// Strip the deadline inherited from the scheduler tool ctx
	// (15min) so the user gets all the time they need to review —
	// same wait policy as ask_user_question, see
	// queryloop.executeClientTools' humanInteractive branch.
	// Cancel signals (session abort, user interrupt) propagate via
	// the engine's separate cancel plumbing — those paths still call
	// SubmitPlanResponse(cancelled) or close the session, both of
	// which unblock requestPlanApproval through req.response or the
	// engine-level shutdown.
	waitCtx := context.WithoutCancel(ctx)
	resp, err := c.deps.QE.requestPlanApproval(waitCtx, sess.ID, out, proposal)
	if err != nil {
		return nil, false, err
	}
	if resp == nil {
		return nil, true, nil
	}
	if !resp.Approved {
		c.deps.Logger.Info("plan coordinator: user rejected plan",
			zap.String("plan_id", proposal.PlanID),
			zap.String("reason", resp.Reason),
		)
		return nil, true, nil
	}

	// Apply user edits if present. Empty UpdatedSteps means "keep
	// proposal as-is".
	if len(resp.UpdatedSteps) == 0 {
		return plan, false, nil
	}
	editedPlan := &Plan{
		Goal:  plan.Goal,
		Steps: fromProposedSteps(resp.UpdatedSteps),
	}
	if err := editedPlan.Validate(); err != nil {
		return nil, true, fmt.Errorf("user-edited plan invalid: %w", err)
	}
	// Validate that every SubagentType (when set) is in availableSubagents.
	// Empty SubagentType is fine — the Scheduler resolves it later via
	// SubagentResolver. Users editing the plan typically don't see this
	// field at all (front-end hides it as of v1.16); the validation only
	// fires for advanced clients that explicitly send a sub-agent name.
	subagentSetMap := make(map[string]struct{}, len(availableSubagents))
	for _, s := range availableSubagents {
		subagentSetMap[s] = struct{}{}
	}
	for _, step := range editedPlan.Steps {
		if step.SubagentType == "" {
			continue
		}
		if _, ok := subagentSetMap[step.SubagentType]; !ok {
			return nil, true, fmt.Errorf("user-edited plan uses unknown subagent_type %q", step.SubagentType)
		}
	}
	c.deps.Logger.Info("plan coordinator: user-edited plan accepted",
		zap.String("plan_id", proposal.PlanID),
		zap.Int("original_steps", len(plan.Steps)),
		zap.Int("edited_steps", len(editedPlan.Steps)),
	)
	return editedPlan, false, nil
}

// askPlanDecisionOnFailure surfaces a plan-level failure (re-plans
// exhausted / planner errored / budget hit) to the user and waits for
// continue / cancel. retry is intentionally NOT offered here: the
// replan loop is itself the retry mechanism; if it exhausted the user's
// remaining option is to either accept whatever was produced
// (continue → fallback aggregation) or stop the run (cancel).
//
// Returns types.StepDecisionContinue when the gate is unavailable so a
// misconfigured deployment degrades to the legacy "silent fallback"
// behaviour rather than blocking forever.
func (c *PlanCoordinator) askPlanDecisionOnFailure(
	ctx context.Context,
	sess *session.Session,
	out chan<- types.EngineEvent,
	lc *loopConfig,
	reason string,
) string {
	if c == nil || c.deps == nil || c.deps.QE == nil || out == nil {
		return types.StepDecisionContinue
	}
	if c.deps.QE.config.DisableStepDecisionGate {
		return types.StepDecisionContinue
	}
	req := &types.StepDecisionRequest{
		RequestID:  newDecisionRequestID(),
		AgentID:    lc.agentID,
		Scope:      types.StepDecisionScopePlan,
		Reason:     reason,
		AllowRetry: false,
	}

	c.deps.Logger.Warn("plan coordinator: pausing on plan failure for user decision",
		zap.String("reason", reason),
	)

	// Same wait policy as plan_review: ctx-deadline-stripped so the user
	// gets unbounded time. Session abort still propagates via the engine
	// shutdown path, which closes the response channel.
	waitCtx := context.WithoutCancel(ctx)
	resp, err := c.deps.QE.requestStepDecision(waitCtx, sess.ID, out, req)
	if err != nil || resp == nil {
		c.deps.Logger.Warn("plan coordinator: decision returned no answer; defaulting to continue (fallback)",
			zap.Error(err),
		)
		return types.StepDecisionContinue
	}
	switch resp.Decision {
	case types.StepDecisionCancel:
		return types.StepDecisionCancel
	default:
		return types.StepDecisionContinue
	}
}

// newPlanID generates a server-side plan identifier. Format mirrors
// art_<24hex> for visual consistency.
func newPlanID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal in any sane runtime.
		panic(fmt.Errorf("plan: crypto/rand failed: %w", err))
	}
	return "pln_" + hex.EncodeToString(b[:])
}

// newDecisionRequestID generates the engine-side identifier for an
// in-flight StepDecisionRequest. Distinct prefix so logs / persisted
// waits stay distinguishable from plan ids.
func newDecisionRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("step decision: crypto/rand failed: %w", err))
	}
	return "sdc_" + hex.EncodeToString(b[:])
}

// toProposedSteps converts engine PlanStep → wire-shape ProposedStep.
// SubagentType is intentionally NOT copied — front-ends don't see the
// executor (v1.16+); the Scheduler resolves it at dispatch time. Servers
// that DO want to expose it (advanced operator UI) can override this
// before emitting the proposal.
func toProposedSteps(steps []*PlanStep) []types.ProposedStep {
	out := make([]types.ProposedStep, len(steps))
	for i, s := range steps {
		out[i] = types.ProposedStep{
			ID:          s.ID,
			Description: s.Description,
			Prompt:      s.Prompt,
			DependsOn:   append([]string(nil), s.DependsOn...),
			// SubagentType deliberately omitted — see func doc.
		}
	}
	return out
}

// fromProposedSteps is the inverse: wire ProposedStep → engine PlanStep.
// ExpectedOutputs is dropped — users can't author them via the wire
// today; the coordinator falls back to the planner's defaults (none).
// SubagentType is preserved if the client did send one (advanced
// override); empty values fall through to runtime resolution.
func fromProposedSteps(steps []types.ProposedStep) []*PlanStep {
	out := make([]*PlanStep, len(steps))
	for i, s := range steps {
		out[i] = &PlanStep{
			ID:           s.ID,
			SubagentType: s.SubagentType,
			Description:  s.Description,
			Prompt:       s.Prompt,
			DependsOn:    append([]string(nil), s.DependsOn...),
		}
	}
	return out
}

// replanEscalation builds the carry-over to hand to the Planner on a
// re-plan iteration. Index 0 = first attempt → use the seed (might be
// from ReAct → Plan promotion). Subsequent indices = use whatever the
// previous outcome produced.
func (c *PlanCoordinator) replanEscalation(replan int, outcome *SchedulerOutcome, lastError string) *EscalationContext {
	if replan == 0 {
		return c.escalation
	}
	if outcome == nil {
		return c.escalation
	}
	ec := &EscalationContext{
		FromMode: CoordinatorModePlan,
		Reason:   lastError,
	}
	for _, r := range outcome.StepResults {
		if r == nil {
			continue
		}
		ec.PriorAttempts = append(ec.PriorAttempts, PriorAttempt{
			Skill:     r.StepID, // best signal we have on StepResult; PriorAttempt.Skill historically meant "executor identifier"
			Status:    r.Status,
			Artifacts: r.Artifacts,
			Failures:  r.Failures,
		})
		ec.PriorArtifacts = append(ec.PriorArtifacts, r.Artifacts...)
	}
	if c.deps.Budget != nil {
		ec.BudgetSpent = c.deps.Budget.Snapshot()
	}
	return ec
}

func (c *PlanCoordinator) budgetExceeded() (bool, string) {
	if c.deps == nil || c.deps.Budget == nil {
		return false, ""
	}
	return c.deps.Budget.Exceeded()
}

// successResult builds the subAgentLoopResult for a passing run.
func (c *PlanCoordinator) successResult(plan *Plan, outcome *SchedulerOutcome, agentID string) subAgentLoopResult {
	var allArt []types.ArtifactRef
	for _, r := range outcome.StepResults {
		if r != nil {
			allArt = append(allArt, r.Artifacts...)
		}
	}
	summary := buildPlanSummary(plan, outcome, "")
	return subAgentLoopResult{
		Terminal: types.Terminal{
			Reason:  types.TerminalCompleted,
			Message: fmt.Sprintf("plan mode: %d steps, all passed", len(plan.Steps)),
		},
		SubmittedArtifacts: allArt,
		CoordinatorMode:    string(CoordinatorModePlan),
		BudgetSpent:        c.snapshotBudget(),
		Summary:            summary,
	}
}

// fallbackResult builds the subAgentLoopResult after running the Fallback
// chain. The result reads as a degraded but honest report — emma can
// surface the partial artifacts plus needs-attention list.
func (c *PlanCoordinator) fallbackResult(plan *Plan, outcome *SchedulerOutcome, reason, agentID string) subAgentLoopResult {
	var stepResults []*StepResult
	if outcome != nil {
		stepResults = outcome.StepResults
	}
	var goal string
	if plan != nil {
		goal = plan.Goal
	}
	fb := c.deps.Fallback.Aggregate(FallbackInput{
		Goal:    goal,
		Reason:  reason,
		Results: stepResults,
		Budget:  c.snapshotBudget(),
	})
	failures := append([]string(nil), fb.NeedsAttention...)
	summary := buildPlanSummary(plan, outcome, fb.Summary)
	return subAgentLoopResult{
		Terminal: types.Terminal{
			Reason:  types.TerminalCompleted, // graceful degrade — not a hard error
			Message: "plan mode: fallback aggregation",
		},
		SubmittedArtifacts: fb.Artifacts,
		ContractFailures:   failures,
		CoordinatorMode:    string(CoordinatorModePlan),
		BudgetSpent:        c.snapshotBudget(),
		Summary:            summary,
	}
}

func (c *PlanCoordinator) snapshotBudget() BudgetSnapshot {
	if c.deps == nil || c.deps.Budget == nil {
		return BudgetSnapshot{}
	}
	return c.deps.Budget.Snapshot()
}

// availableSubagentsForPlanner pulls the L3 skill set from the engine's
// AgentDefinitionRegistry. Empty means "any skill" — the heuristic
// planner falls through to single-step in that case.
func availableSubagentsForPlanner(deps *SharedDeps) []string {
	if deps == nil || deps.QE == nil || deps.QE.defRegistry == nil {
		return nil
	}
	listings := deps.QE.defRegistry.ListForPlanner()
	out := make([]string, 0, len(listings))
	for _, l := range listings {
		out = append(out, l.Name)
	}
	return out
}

// buildPlanSummary composes the <summary>...</summary> block that emma
// quotes back to the user. Replaces the previously-empty Output for
// plan-mode runs so the L1 layer doesn't fabricate filenames or copy
// preview text.
//
// Format mirrors the worker / scheduler summary contract:
//   - leading <summary> tag
//   - one line per artifact: "- [role] art_xxx — name"
//   - optional fallback text (when plan failed and FallbackChain
//     produced its own narrative)
//
// fallbackText is non-empty when called from fallbackResult — passed
// through verbatim so the user sees the same explanation the
// FallbackChain.Aggregate built ("降级原因：…").
func buildPlanSummary(plan *Plan, outcome *SchedulerOutcome, fallbackText string) string {
	var b strings.Builder
	b.WriteString("<summary>\n")
	if fallbackText != "" {
		b.WriteString(fallbackText)
		b.WriteString("\n")
	} else if plan != nil && outcome != nil {
		fmt.Fprintf(&b, "按 %d 步计划完成。\n", len(plan.Steps))
	} else {
		b.WriteString("plan-mode run produced no output.\n")
	}

	if outcome != nil {
		seen := map[string]struct{}{}
		for _, r := range outcome.StepResults {
			if r == nil {
				continue
			}
			for _, a := range r.Artifacts {
				if _, dup := seen[a.ArtifactID]; dup {
					continue
				}
				seen[a.ArtifactID] = struct{}{}
				role := a.Role
				if role == "" {
					role = "artifact"
				}
				name := a.Name
				if name == "" {
					name = a.ArtifactID
				}
				fmt.Fprintf(&b, "- [%s] %s — %s\n", role, a.ArtifactID, name)
			}
		}
	}
	b.WriteString("</summary>")
	return b.String()
}

// extractGoal pulls the natural-language task seed from loopConfig.
// originalPrompt is what cfg.Prompt was at SpawnSync time; for
// the scheduler this is exactly the task emma dispatched. Falls back to
// the subagent type label when prompt is empty (legacy / programmatic
// spawns without a free-form task).
func extractGoal(lc *loopConfig) string {
	if lc == nil {
		return ""
	}
	if lc.originalPrompt != "" {
		return lc.originalPrompt
	}
	return lc.subagentType
}

