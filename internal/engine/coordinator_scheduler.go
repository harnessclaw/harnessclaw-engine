package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

// defaultStepMaxAttempts is the fallback when EngineConfig.MaxStepAttempts
// is zero (tests / legacy callers that never set it). Production reads
// the config-driven value from QueryEngine; non-transient failures
// (contract violations, dependency failures, resolution errors) skip
// retry entirely regardless — see isTransientFailure.
const defaultStepMaxAttempts = 3

// SchedulerOutcome bundles what one Scheduler.Run produced. Mirrors the
// shape Plan / Fallback consumers want: per-step results so Judge can
// review, an aggregate Status so the Coordinator can branch, and the
// captured budget snapshot.
type SchedulerOutcome struct {
	StepResults []*StepResult
	Status      string // "success" | "partial" | "failed"
	Reason      string
	Budget      BudgetSnapshot
}

// Scheduler executes a Plan by dispatching L3 sub-agents step-by-step.
// The current implementation is sequential (single-threaded) — Phase B
// scope. Parallel / Race / Vote dispatch strategies plug in via
// DispatchStrategy registry in a follow-up.
//
// Scheduler does NOT reach into QueryEngine internals beyond what
// SpawnSync exposes; this keeps the scheduler testable with a fake
// SubAgentDispatcher that doesn't need a full QueryEngine.
type Scheduler struct {
	deps     *SharedDeps
	dispatch SubAgentDispatcher
	logger   *zap.Logger
}

// SubAgentDispatcher is the seam through which Scheduler spawns L3 sub-agents.
// In production it's a thin adapter over QueryEngine.SpawnSync; tests
// inject a fake to drive Scheduler without booting the engine.
type SubAgentDispatcher interface {
	Dispatch(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error)
}

// NewScheduler builds a Scheduler. dispatch may be nil to use the engine's
// default (SpawnSync). Logger may be nil for tests.
func NewScheduler(deps *SharedDeps, dispatch SubAgentDispatcher, logger *zap.Logger) *Scheduler {
	if logger == nil {
		logger = zap.NewNop()
	}
	if dispatch == nil && deps != nil && deps.QE != nil {
		dispatch = &queryEngineDispatcher{qe: deps.QE}
	}
	return &Scheduler{
		deps:     deps,
		dispatch: dispatch,
		logger:   logger.Named("scheduler"),
	}
}

// Run executes the plan sequentially. Each step waits for its DependsOn
// predecessors to complete; on failure of a step, downstream steps that
// depend on it are marked "skipped" without dispatching.
//
// Per-step Judge L1 runs after each successful submission; if the step's
// contract isn't met, Run records the failure and continues — the
// Coordinator decides whether to re-plan or fall back.
//
// Budget is consulted at the start of every step. If exceeded, Run
// stops issuing further steps and returns whatever has accumulated.
func (s *Scheduler) Run(
	ctx context.Context,
	plan *Plan,
	parentCfg *agent.SpawnConfig,
) *SchedulerOutcome {
	outcome := &SchedulerOutcome{Status: "success"}
	if plan == nil || len(plan.Steps) == 0 {
		outcome.Status = "failed"
		outcome.Reason = "empty plan"
		return outcome
	}

	// Map step ID → result; consulted to compute "is X's deps satisfied?"
	results := make(map[string]*StepResult, len(plan.Steps))

	for _, step := range plan.Steps {
		// Budget gate.
		if s.deps != nil && s.deps.Budget != nil {
			if exceeded, why := s.deps.Budget.Exceeded(); exceeded {
				outcome.Status = "partial"
				outcome.Reason = why
				outcome.Budget = s.deps.Budget.Snapshot()
				s.logger.Warn("scheduler: budget exceeded mid-plan",
					zap.String("step_id", step.ID),
					zap.String("reason", why),
				)
				return finalize(outcome, results, plan, "budget exceeded — skipped", pickOut(parentCfg))
			}
		}

		// Skip step whose deps failed.
		if dep, missing := unsatisfiedDep(step, results); missing {
			reason := fmt.Sprintf("upstream dep %s did not succeed", dep)
			s.logger.Info("scheduler: skipping step due to upstream failure",
				zap.String("step_id", step.ID),
				zap.String("missing_dep", dep),
			)
			results[step.ID] = &StepResult{
				StepID:   step.ID,
				Status:   "skipped",
				Failures: []string{reason},
			}
			outcome.StepResults = append(outcome.StepResults, results[step.ID])
			emitStepSkipped(pickOut(parentCfg), step, reason)
			continue
		}

		// Dispatch with step-level retry on transient failures. This
		// keeps a single timeout / 5xx / rate_limit from forcing the
		// whole plan to re-plan from scratch.
		result := s.dispatchStepWithRetry(ctx, plan, step, results, parentCfg)
		results[step.ID] = result
		outcome.StepResults = append(outcome.StepResults, result)
		emitStepResult(pickOut(parentCfg), step, result)

		// Consolidated step-failure log. The sub-paths inside dispatchStep
		// already log component-specific Warns (resolution / dispatch error
		// / terminal failure / missing artifacts), but only this line ties
		// the final outcome together with attempt count and ErrorType so an
		// operator scanning scheduler logs sees one unambiguous "step X
		// failed after N attempts" entry per failed step.
		if result != nil && result.Status == "failed" {
			s.logger.Error("scheduler: step failed",
				zap.String("step_id", step.ID),
				zap.String("subagent_type", step.SubagentType),
				zap.Int("attempts", result.Attempts),
				zap.Bool("retryable", isTransientFailure(result)),
				zap.String("error_type", string(classifyStepErrorType(result.Failures))),
				zap.Strings("failure_sample", failureSample(result.Failures, 3)),
			)
		}

		// Per-step Judge.
		if s.deps != nil && s.deps.Judge != nil {
			verdict := s.deps.Judge.ReviewStep(step, result)
			if !verdict.Pass {
				s.logger.Warn("scheduler: per-step judge failed",
					zap.String("step_id", step.ID),
					zap.Strings("reasons", verdict.Reasons),
				)
				// Mark step as failed so downstream skipping kicks in.
				if result.Status == "success" {
					result.Status = "failed"
					result.Failures = append(result.Failures, verdict.Reasons...)
				}
				if s.deps.Budget != nil {
					s.deps.Budget.NoteFailure()
				}
			}
		}

		// User-decision gate. When a step exits with status=failed, do
		// NOT silently move on to the next step — pause and ask the
		// user to decide. This replaces the old "skip and aggregate
		// partial result" behaviour that left users with sunk-cost
		// surprise (LLM tokens already paid, but the run abandoned its
		// remaining work). User picks:
		//   continue → accept the failure, keep running (this is the
		//              old behaviour, now opt-in)
		//   retry    → run dispatchStepWithRetry again on this step
		//   cancel   → stop the whole plan, route to fallback
		if result != nil && result.Status == "failed" {
			decision := s.askStepDecisionOnFailure(ctx, step, result, parentCfg)
			switch decision {
			case types.StepDecisionRetry:
				retried := s.dispatchStepWithRetry(ctx, plan, step, results, parentCfg)
				results[step.ID] = retried
				outcome.StepResults[len(outcome.StepResults)-1] = retried
				emitStepResult(pickOut(parentCfg), step, retried)
				result = retried
			case types.StepDecisionCancel:
				outcome.Status = "cancelled"
				outcome.Reason = "user cancelled after step " + step.ID + " failed"
				if s.deps != nil && s.deps.Budget != nil {
					outcome.Budget = s.deps.Budget.Snapshot()
				}
				return finalize(outcome, results, plan, "cancelled by user", pickOut(parentCfg))
			}
			// types.StepDecisionContinue (or unknown) falls through —
			// dependents are skipped naturally on the next iteration via
			// unsatisfiedDep.
		}
	}

	if s.deps != nil && s.deps.Budget != nil {
		outcome.Budget = s.deps.Budget.Snapshot()
	}
	classifyOutcome(outcome)
	return outcome
}

// dispatchStepWithRetry runs dispatchStep up to stepMaxAttempts times
// when the failure is transient. Per-step retry is the right granularity
// for flakes: cheaper than re-planning the whole graph and avoids the
// "successful steps re-run because one peer flaked" anti-pattern that
// the plan-level replan loop alone produces.
//
// Retry policy:
//   - attempt 1: always
//   - attempt 2: only if isTransientFailure(result) — see classification rules
//   - non-transient failures (contract / dependency / resolution / invalid input)
//     return immediately so the plan-level loop can decide to re-plan
//
// emit step.started fires on each attempt so the client UI can render
// "retrying step X (attempt 2/2)". The final emit step.completed/failed
// carries the cumulative Attempts count.
func (s *Scheduler) dispatchStepWithRetry(
	ctx context.Context,
	plan *Plan,
	step *PlanStep,
	prior map[string]*StepResult,
	parentCfg *agent.SpawnConfig,
) *StepResult {
	out := pickOut(parentCfg)
	maxAttempts := s.maxStepAttempts()
	var result *StepResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		emitStepStarted(out, step, attempt)
		result = s.dispatchStep(ctx, plan, step, prior, parentCfg)
		result.Attempts = attempt
		if result.Status == "success" {
			return result
		}
		if !isTransientFailure(result) {
			s.logger.Debug("scheduler: step failure is non-transient, not retrying",
				zap.String("step_id", step.ID),
				zap.Strings("failures", result.Failures),
			)
			return result
		}
		if attempt < maxAttempts {
			s.logger.Info("scheduler: retrying step after transient failure",
				zap.String("step_id", step.ID),
				zap.Int("next_attempt", attempt+1),
				zap.Strings("failures", result.Failures),
			)
		}
	}
	return result
}

// maxStepAttempts returns the per-step retry cap, honouring an
// EngineConfig override when wired through the engine and falling back
// to defaultStepMaxAttempts otherwise (tests / legacy callers).
func (s *Scheduler) maxStepAttempts() int {
	if s != nil && s.deps != nil && s.deps.QE != nil && s.deps.QE.config.MaxStepAttempts > 0 {
		return s.deps.QE.config.MaxStepAttempts
	}
	return defaultStepMaxAttempts
}

// askStepDecisionOnFailure surfaces a step failure to the user via the
// QueryEngine's prompt.user pipeline and waits for the response. Returns
// types.StepDecisionContinue when the gate is unavailable (no QE, no
// ParentOut, ctx already done) so a misconfigured deployment degrades to
// the legacy "skip and continue" behaviour rather than blocking forever.
func (s *Scheduler) askStepDecisionOnFailure(
	ctx context.Context,
	step *PlanStep,
	result *StepResult,
	parentCfg *agent.SpawnConfig,
) string {
	if s == nil || s.deps == nil || s.deps.QE == nil {
		return types.StepDecisionContinue
	}
	if s.deps.QE.config.DisableStepDecisionGate {
		return types.StepDecisionContinue
	}
	out := pickOut(parentCfg)
	if out == nil {
		return types.StepDecisionContinue
	}
	sessionID := pickStr(parentCfg, func(c *agent.SpawnConfig) string { return c.ParentSessionID })
	reqID := newDecisionRequestID()

	reason := summariseFailures(result)
	req := &types.StepDecisionRequest{
		RequestID:       reqID,
		AgentID:         pickStr(parentCfg, func(c *agent.SpawnConfig) string { return c.Name }),
		Scope:           types.StepDecisionScopeStep,
		StepID:          step.ID,
		StepDescription: step.Description,
		Reason:          reason,
		Attempts:        result.Attempts,
		AllowRetry:      true,
	}

	// Strip inherited ctx deadlines so the user gets unbounded time —
	// same wait policy as plan_review / ask_user_question. Cancellation
	// via session abort still propagates through SubmitStepDecision /
	// engine shutdown, which closes the response channel.
	waitCtx := context.WithoutCancel(ctx)

	s.logger.Warn("scheduler: pausing on step failure for user decision",
		zap.String("step_id", step.ID),
		zap.Int("attempts", result.Attempts),
		zap.String("reason", reason),
	)

	resp, err := s.deps.QE.requestStepDecision(waitCtx, sessionID, out, req)
	if err != nil || resp == nil {
		s.logger.Warn("scheduler: step decision request returned no answer; defaulting to continue",
			zap.String("step_id", step.ID),
			zap.Error(err),
		)
		return types.StepDecisionContinue
	}
	switch resp.Decision {
	case types.StepDecisionContinue, types.StepDecisionRetry, types.StepDecisionCancel:
		s.logger.Info("scheduler: step decision answered",
			zap.String("step_id", step.ID),
			zap.String("decision", resp.Decision),
			zap.String("note", resp.Note),
		)
		return resp.Decision
	default:
		s.logger.Warn("scheduler: unrecognised step decision; treating as cancel",
			zap.String("step_id", step.ID),
			zap.String("decision", resp.Decision),
		)
		return types.StepDecisionCancel
	}
}

// summariseFailures collapses a StepResult.Failures slice into a single
// short line for surfacing in the user prompt. Truncates at 200 chars
// so a verbose stack-trace failure doesn't blow out the wire payload.
func summariseFailures(r *StepResult) string {
	if r == nil || len(r.Failures) == 0 {
		return ""
	}
	first := strings.TrimSpace(r.Failures[0])
	if len(first) > 200 {
		first = first[:200] + "…"
	}
	if len(r.Failures) == 1 {
		return first
	}
	return fmt.Sprintf("%s (+%d more)", first, len(r.Failures)-1)
}

// isTransientFailure inspects a StepResult's failure messages and decides
// whether retrying could plausibly succeed. Errors fall into one of three
// buckets:
//
//   transient (retry helps):
//     - timeout / deadline exceeded
//     - rate_limit / overloaded / 503 / 504
//     - connection refused / reset / network unreachable
//
//   non-transient (retry wastes resources):
//     - contract violations (sub-agent failed submit_task_result schema)
//     - dependency failures (upstream step skipped)
//     - resolution failures (no L3 matches the step)
//     - invalid input (4xx-class)
//
// The match is keyword-based on combined Failures text — sub-agent error
// strings are unstructured today, so a deeper classifier would be over-
// engineering. As we move to structured emit.ErrorInfo end-to-end the
// classifier can switch to pattern-matching on ErrorType.
func isTransientFailure(r *StepResult) bool {
	if r == nil || r.Status != "failed" || len(r.Failures) == 0 {
		return false
	}
	combined := strings.ToLower(strings.Join(r.Failures, " "))
	transientMarkers := []string{
		"timeout", "deadline exceeded",
		"rate limit", "rate_limit", "ratelimit",
		"overloaded", "overload",
		"503", "504", "502",
		"connection refused", "connection reset", "broken pipe",
		"network unreachable", "tls handshake",
		"context deadline",
		// Terminal-class transient signals surfaced by appendTerminalFailure.
		// blocking_limit = provider rate-limit / credit exhaustion (often
		// recovers within seconds); model_error covers transient 5xx + stream
		// truncation; image_error covers Bedrock-style transient image
		// reprocessing failures.
		"terminal_blocking_limit",
		"terminal_model_error",
		"terminal_image_error",
	}
	for _, m := range transientMarkers {
		if strings.Contains(combined, m) {
			return true
		}
	}
	return false
}

// dispatchStep builds the SpawnConfig for one step and calls the
// dispatcher. Step-level metadata (ID, SubagentType, Prompt,
// ExpectedOutputs) maps to fields the L3 driver already understands.
//
// Resolves SubagentType at dispatch time when the step left it empty
// (the v1.16 default for LLMPlanner-produced plans). Resolution uses
// SharedDeps.SubagentResolver; the chosen executor is stamped onto
// step.SubagentType so re-runs / observability see a consistent value.
func (s *Scheduler) dispatchStep(
	ctx context.Context,
	plan *Plan,
	step *PlanStep,
	prior map[string]*StepResult,
	parentCfg *agent.SpawnConfig,
) *StepResult {
	if step.SubagentType == "" {
		if err := s.resolveSubagentForStep(ctx, step); err != nil {
			s.logger.Error("scheduler: subagent resolution failed",
				zap.String("step_id", step.ID),
				zap.Error(err),
			)
			return &StepResult{
				StepID:   step.ID,
				Status:   "failed",
				Failures: []string{"subagent resolution: " + err.Error()},
			}
		}
	}

	// step_dispatched is emit-only on the Scheduler side — by the time
	// we hit this line, both SubagentType (resolved) and the seed prompt
	// are stable and the L3 driver hasn't started yet.
	emitStepDispatched(pickOut(parentCfg), step)

	prompt := buildStepPrompt(step, prior)

	rootSID := pickStr(parentCfg, func(c *agent.SpawnConfig) string { return c.RootSessionID })
	_, inputPaths, readScope, writeScope := workspace.L3SpawnScope(workspaceRootDir(), rootSID, step.ID, step.DependsOn)

	cfg := &agent.SpawnConfig{
		Prompt:           prompt,
		AgentType:        tool.AgentTypeSync,
		SubagentType:     step.SubagentType,
		Description:      step.Description,
		Name:             fmt.Sprintf("plan-step-%s", step.ID),
		ParentSessionID:  pickStr(parentCfg, func(c *agent.SpawnConfig) string { return c.ParentSessionID }),
		RootSessionID:    rootSID,
		ParentOut:        pickOut(parentCfg),
		// Timeout is intentionally omitted: the per-step 15-min wall clock
		// killed legitimate long sub-agent runs mid-flight (token already
		// paid). Cancellation now flows only via the parent ctx (user
		// abort / session shutdown). Genuinely-stuck steps are caught by
		// the lifecycle watchdog (5-min idle on the step card) and the
		// failure-decision gate at the scheduler level.
		ExpectedOutputs: step.ExpectedOutputs,
		// TaskID is step.ID directly (clean identifier matching
		// plan.json/tasks/{id}/). The previous "plan-{goal}-{step.ID}"
		// composite couldn't pass workspace.mustSafe because plan.Goal is
		// free user text.
		TaskID:        step.ID,
		TaskStartedAt: time.Now(),
		ParentStepID:  step.ID,
		InputPaths:    inputPaths,
		ReadScope:     readScope,
		WriteScope:    writeScope,
	}

	res, err := s.dispatch.Dispatch(ctx, cfg)
	if err != nil {
		s.logger.Error("scheduler: dispatch error",
			zap.String("step_id", step.ID),
			zap.Error(err),
		)
		return &StepResult{
			StepID:  step.ID,
			Status:  "failed",
			Failures: []string{err.Error()},
		}
	}

	// D14 post-spawn reconciliation: read the L3's meta.json (if any)
	// and update plan.json accordingly. Done in a separate step from the
	// step-result bookkeeping so a meta-read failure doesn't mask the
	// scheduler's view of the spawn outcome.
	if root := workspaceRootDir(); root != "" && rootSID != "" {
		if _, recErr := workspace.ReconcileSpawnReturn(ctx, planWriterRegistry().Get(rootSID), root, rootSID, step.ID); recErr != nil {
			s.logger.Warn("scheduler: plan reconcile failed (non-fatal)",
				zap.String("step_id", step.ID),
				zap.Error(recErr),
			)
		}
	}

	// Bookkeeping for budget tracker.
	if s.deps != nil && s.deps.Budget != nil && res.Usage != nil {
		s.deps.Budget.AddUsage(res.Usage)
	}

	out := &StepResult{
		StepID:    step.ID,
		Summary:   parseSummary(res.Output),
		Artifacts: res.SubmittedArtifacts,
		Failures:  res.ContractFailures,
		Usage:     res.Usage,
	}
	switch {
	case agent.IsTerminalError(res):
		out.Status = "failed"
		// Without this, terminal-class failures (model_error / blocking_limit /
		// prompt_too_long / max_turns + contract violations) leave Failures
		// empty when ContractFailures is empty — emit_step_failed then ships
		// no Reason / Error and the retry classifier can't see the transient
		// signal. Synthesize a "<reason>: <message>" failure line so the wire
		// event is informative and isTransientFailure can match.
		out.Failures = appendTerminalFailure(out.Failures, res)
		s.logger.Error("scheduler: step ended with terminal failure",
			zap.String("step_id", step.ID),
			zap.String("subagent_type", step.SubagentType),
			zap.String("terminal_reason", terminalReasonOf(res)),
			zap.String("terminal_message", truncForLog(terminalMessageOf(res), 200)),
			zap.Int("contract_failures", len(res.ContractFailures)),
			zap.Strings("failure_sample", failureSample(res.ContractFailures, 3)),
		)
	case len(res.SubmittedArtifacts) == 0 && len(step.ExpectedOutputs) > 0:
		// Sub-agent returned without producing required artifacts — count
		// as failure even if Terminal looks "completed".
		out.Status = "failed"
		out.Failures = append(out.Failures, "step produced no artifacts despite expected outputs")
		s.logger.Error("scheduler: step missing required artifacts",
			zap.String("step_id", step.ID),
			zap.String("subagent_type", step.SubagentType),
			zap.Int("expected_outputs", len(step.ExpectedOutputs)),
			zap.Strings("expected_roles", expectedRolesOf(step.ExpectedOutputs)),
		)
	default:
		out.Status = "success"
	}
	return out
}

// appendTerminalFailure synthesizes a failure line from the SpawnResult's
// Terminal block so callers see *something* in StepResult.Failures even
// when the sub-agent didn't produce ContractFailures. Format mirrors the
// agent.BuildFailureContent header so the same string survives both wire
// event and dispatch.out log.
func appendTerminalFailure(existing []string, res *agent.SpawnResult) []string {
	if res == nil || res.Terminal == nil {
		return existing
	}
	reason := string(res.Terminal.Reason)
	if reason == "" {
		return existing
	}
	line := "terminal_" + reason
	if msg := strings.TrimSpace(res.Terminal.Message); msg != "" {
		line = line + ": " + msg
	}
	return append(existing, line)
}

func terminalReasonOf(res *agent.SpawnResult) string {
	if res == nil || res.Terminal == nil {
		return ""
	}
	return string(res.Terminal.Reason)
}

func terminalMessageOf(res *agent.SpawnResult) string {
	if res == nil || res.Terminal == nil {
		return ""
	}
	return res.Terminal.Message
}

func expectedRolesOf(outs []types.ExpectedOutput) []string {
	if len(outs) == 0 {
		return nil
	}
	roles := make([]string, 0, len(outs))
	for _, o := range outs {
		roles = append(roles, o.Role)
	}
	return roles
}

// failureSample is the same shape as the helper in tool/scheduler,
// duplicated locally so the scheduler doesn't import a tool package
// (would create a cycle). Keeps the Warn line readable.
func failureSample(failures []string, n int) []string {
	if len(failures) == 0 {
		return nil
	}
	if len(failures) <= n {
		out := make([]string, len(failures))
		for i, f := range failures {
			out[i] = truncForLog(f, 120)
		}
		return out
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = truncForLog(failures[i], 120)
	}
	return out
}

// resolveSubagentForStep fills step.SubagentType using SharedDeps.SubagentResolver.
// Stamps the chosen value onto step so re-runs / observability see the
// same picked executor. Returns an error only when no resolver is wired
// AND the registry has no fallback agent at all — both conditions
// indicate misconfiguration, not legitimate runtime failure.
func (s *Scheduler) resolveSubagentForStep(ctx context.Context, step *PlanStep) error {
	available := availableSubagentsForPlanner(s.deps)
	if len(available) == 0 {
		return fmt.Errorf("no L3 sub-agents registered")
	}

	resolver := SubagentResolver(nil)
	if s.deps != nil {
		resolver = s.deps.SubagentResolver
	}
	if resolver == nil {
		resolver = NewHeuristicSubagentResolver()
	}

	// Goal text for resolver: prefer prompt (more specific), fall back
	// to description, then plan-level goal would require carrying it
	// in here — keeping the resolution local to step.
	goal := step.Prompt
	if strings.TrimSpace(goal) == "" {
		goal = step.Description
	}

	pick, reason, err := resolver.Resolve(ctx, goal, available)
	if err != nil {
		return err
	}
	step.SubagentType = pick
	s.logger.Info("scheduler: resolved subagent for step",
		zap.String("step_id", step.ID),
		zap.String("subagent", pick),
		zap.String("reason", reason),
	)
	return nil
}

// queryEngineDispatcher is the production implementation of
// SubAgentDispatcher. Just unwraps QueryEngine.SpawnSync.
type queryEngineDispatcher struct {
	qe *QueryEngine
}

func (d *queryEngineDispatcher) Dispatch(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return d.qe.SpawnSync(ctx, cfg)
}

// unsatisfiedDep returns the first dep ID that didn't succeed, or "" + false.
func unsatisfiedDep(step *PlanStep, prior map[string]*StepResult) (string, bool) {
	for _, dep := range step.DependsOn {
		r, ok := prior[dep]
		if !ok || r == nil || r.Status != "success" {
			return dep, true
		}
	}
	return "", false
}

// classifyOutcome looks across StepResults and sets the rolled-up Status.
// Pure success requires every step to have status==success. Anything in
// between is "partial". All-failed is "failed".
func classifyOutcome(o *SchedulerOutcome) {
	if o.Status != "success" {
		// Already pre-set (e.g. budget exceeded) — keep it.
		return
	}
	var ok, fail int
	for _, r := range o.StepResults {
		switch r.Status {
		case "success":
			ok++
		case "failed", "skipped":
			fail++
		}
	}
	switch {
	case fail == 0:
		o.Status = "success"
	case ok == 0:
		o.Status = "failed"
	default:
		o.Status = "partial"
	}
}

// finalize marks any remaining un-dispatched step as skipped and emits
// the corresponding step_skipped wire event so the front-end can move
// each step from "queued" to "skipped" — used when the scheduler bails
// out early (budget exceeded / user cancelled at a step gate).
//
// Without the wire emission these steps stayed visually "queued"
// forever in the UI: their card.add never fired (no dispatch happened)
// AND no card.close fired either, so the front-end's per-step status
// derivation from card.close events left them stuck. The
// outcome.StepResults entry alone isn't visible on the wire.
//
// out may be nil (tests / spawns without a ParentOut) — emitStepSkipped
// guards against that.
func finalize(
	o *SchedulerOutcome,
	results map[string]*StepResult,
	plan *Plan,
	reason string,
	out chan<- types.EngineEvent,
) *SchedulerOutcome {
	for _, step := range plan.Steps {
		if _, done := results[step.ID]; done {
			continue
		}
		results[step.ID] = &StepResult{StepID: step.ID, Status: "skipped", Failures: []string{reason}}
		o.StepResults = append(o.StepResults, results[step.ID])
		emitStepSkipped(out, step, reason)
	}
	classifyOutcome(o)
	return o
}

// buildStepPrompt enriches a step's seed prompt with references to
// upstream artifacts so the L3 sub-agent knows what's available without
// re-doing work. Strict in scope — we only mention IDs and roles, the
// sub-agent's own preamble injection (composeArtifactPreamble) does the
// heavy lifting of describing each.
func buildStepPrompt(step *PlanStep, prior map[string]*StepResult) string {
	if len(step.DependsOn) == 0 {
		return step.Prompt
	}
	var b strings.Builder
	b.WriteString(step.Prompt)
	b.WriteString("\n\n上游产出（已 trace 内可读，按需 ArtifactRead）：\n")
	for _, dep := range step.DependsOn {
		r := prior[dep]
		if r == nil {
			continue
		}
		for _, a := range r.Artifacts {
			fmt.Fprintf(&b, "- step %s [%s] %s\n", dep, refRole(a), a.ArtifactID)
		}
	}
	return b.String()
}

func refRole(a types.ArtifactRef) string {
	if a.Role == "" {
		return "(no role)"
	}
	return a.Role
}

// parseSummary extracts the <summary>...</summary> body from an L3 sub-agent
// final output. Falls back to the first 200 chars if no tag is found.
// Implementation lives in subagent.go — we reuse it via the package-level
// helper to avoid duplication.
func parseSummary(s string) string {
	out := parseSummaryTag(s)
	if out != "" {
		return out
	}
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// pickStr / pickOut are defensive helpers for accessing fields on a
// possibly-nil parentCfg. The Scheduler can be invoked from tests with
// nil parent — we don't want to crash; just propagate empty.
func pickStr(c *agent.SpawnConfig, accessor func(*agent.SpawnConfig) string) string {
	if c == nil {
		return ""
	}
	return accessor(c)
}

func pickOut(c *agent.SpawnConfig) chan<- types.EngineEvent {
	if c == nil {
		return nil
	}
	return c.ParentOut
}

// --- Step lifecycle emit helpers ---

// emitStepDispatched fires the step_dispatched event right before the
// L3 sub-agent is spawned. Carries the resolved SubagentType + a short
// input summary so the client can render "step s1 → researcher: …"
// before any sub-agent events arrive.
func emitStepDispatched(out chan<- types.EngineEvent, step *PlanStep) {
	if out == nil || step == nil {
		return
	}
	out <- types.EngineEvent{
		Type: types.EngineEventStepDispatched,
		TaskDispatch: &types.TaskDispatch{
			TaskID:       step.ID,
			SubagentType: step.SubagentType,
			InputSummary: truncForLog(step.Description, 120),
		},
	}
}

// emitStepStarted fires step_started immediately before the L3 sub-
// agent is invoked, after dispatch routing has settled. Lets the client
// distinguish "queued / waiting on a wave" (only step.dispatched seen)
// from "actually executing" (step.started observed). Attempt > 1
// signals a transient-failure retry — clients should update the
// step card to "retrying" rather than treating it as a fresh step.
func emitStepStarted(out chan<- types.EngineEvent, step *PlanStep, attempt int) {
	if out == nil || step == nil {
		return
	}
	out <- types.EngineEvent{
		Type: types.EngineEventStepStarted,
		TaskDispatch: &types.TaskDispatch{
			TaskID:       step.ID,
			SubagentType: step.SubagentType,
			Attempts:     attempt,
		},
	}
}

// emitStepResult fires step_completed / step_failed based on the step's
// final result. Two events shaped the same way (TaskDispatch payload),
// distinguished by EngineEventType — keeps the wire shape predictable.
func emitStepResult(out chan<- types.EngineEvent, step *PlanStep, res *StepResult) {
	if out == nil || step == nil || res == nil {
		return
	}
	switch res.Status {
	case "success":
		out <- types.EngineEvent{
			Type: types.EngineEventStepCompleted,
			TaskDispatch: &types.TaskDispatch{
				TaskID:        step.ID,
				SubagentType:  step.SubagentType,
				OutputSummary: truncForLog(res.Summary, 200),
				Attempts:      res.Attempts,
			},
		}
	case "failed":
		// First failure entry is the most informative; include all in
		// the developer-facing Error field for debugging.
		var errMsg string
		if len(res.Failures) > 0 {
			errMsg = res.Failures[0]
		}
		errType := classifyStepErrorType(res.Failures)
		out <- types.EngineEvent{
			Type: types.EngineEventStepFailed,
			TaskDispatch: &types.TaskDispatch{
				TaskID:       step.ID,
				SubagentType: step.SubagentType,
				ErrorType:    string(errType),
				Error:        errMsg,
				Reason:       strings.Join(res.Failures, "; "),
				Retryable:    isTransientFailure(res),
				Attempts:     res.Attempts,
			},
		}
	case "skipped":
		// Skipped path is handled at Run-time before dispatch via
		// emitStepSkipped — but we still fall through here for
		// defensive double-firing.
		emitStepSkipped(out, step, strings.Join(res.Failures, "; "))
	}
}

// classifyStepErrorType maps free-form step failure strings to the
// controlled emit.ErrorType enum so wire consumers (monitoring, UI
// tooltips) can route on a stable value instead of substring-matching
// English. The classifier inherits the same keyword vocabulary as
// isTransientFailure to keep the two views consistent: anything we
// would retry maps to a transient enum value, anything we would not
// maps to a permanent one.
func classifyStepErrorType(failures []string) emit.ErrorType {
	if len(failures) == 0 {
		return emit.ErrorTypeInternal
	}
	combined := strings.ToLower(strings.Join(failures, " "))
	switch {
	case strings.Contains(combined, "rate limit"),
		strings.Contains(combined, "rate_limit"),
		strings.Contains(combined, "ratelimit"),
		strings.Contains(combined, "terminal_blocking_limit"):
		return emit.ErrorTypeToolRateLimited
	case strings.Contains(combined, "overload"),
		strings.Contains(combined, "503"),
		strings.Contains(combined, "502"),
		strings.Contains(combined, "terminal_model_error"):
		return emit.ErrorTypeOverloaded
	case strings.Contains(combined, "timeout"),
		strings.Contains(combined, "deadline"),
		strings.Contains(combined, "504"):
		return emit.ErrorTypeToolTimeout
	case strings.Contains(combined, "subagent resolution"),
		strings.Contains(combined, "no l3"),
		strings.Contains(combined, "no available"):
		return emit.ErrorTypeDependencyFail
	case strings.Contains(combined, "upstream dep"),
		strings.Contains(combined, "did not succeed"):
		return emit.ErrorTypeDependencyFail
	case strings.Contains(combined, "contract"),
		strings.Contains(combined, "expected outputs"),
		strings.Contains(combined, "submit_task"):
		return emit.ErrorTypeInternal // contract_fail not in v1 enum; reuse Internal
	case strings.Contains(combined, "invalid"),
		strings.Contains(combined, "schema"):
		return emit.ErrorTypeToolInvalidInput
	}
	return emit.ErrorTypeInternal
}

// emitStepSkipped fires step_skipped when an upstream dep failure or a
// budget gate prevents this step from running. reason is surfaced to
// the client UI.
func emitStepSkipped(out chan<- types.EngineEvent, step *PlanStep, reason string) {
	if out == nil || step == nil {
		return
	}
	out <- types.EngineEvent{
		Type: types.EngineEventStepSkipped,
		TaskDispatch: &types.TaskDispatch{
			TaskID:       step.ID,
			SubagentType: step.SubagentType,
			Reason:       reason,
		},
	}
}
