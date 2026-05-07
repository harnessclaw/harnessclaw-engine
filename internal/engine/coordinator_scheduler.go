package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

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
				return finalize(outcome, results, plan, "budget exceeded — skipped")
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

		// Dispatch.
		result := s.dispatchStep(ctx, plan, step, results, parentCfg)
		results[step.ID] = result
		outcome.StepResults = append(outcome.StepResults, result)
		emitStepResult(pickOut(parentCfg), step, result)

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
	}

	if s.deps != nil && s.deps.Budget != nil {
		outcome.Budget = s.deps.Budget.Snapshot()
	}
	classifyOutcome(outcome)
	return outcome
}

// dispatchStep builds the SpawnConfig for one step and calls the
// dispatcher. Step-level metadata (ID, SubagentType, Prompt,
// ExpectedOutputs) maps to fields the L3 driver already understands.
//
// Resolves SubagentType at dispatch time when the step left it empty
// (the v1.16 default for HeuristicPlanner-produced plans). Resolution
// uses SharedDeps.SubagentResolver; the chosen executor is stamped onto
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
			s.logger.Warn("scheduler: subagent resolution failed",
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

	cfg := &agent.SpawnConfig{
		Prompt:           prompt,
		AgentType:        tool.AgentTypeSync,
		SubagentType:     step.SubagentType,
		Description:      step.Description,
		Name:             fmt.Sprintf("plan-step-%s", step.ID),
		ParentSessionID:  pickStr(parentCfg, func(c *agent.SpawnConfig) string { return c.ParentSessionID }),
		ParentOut:        pickOut(parentCfg),
		Timeout:          15 * time.Minute,
		ExpectedOutputs:  step.ExpectedOutputs,
		TaskID:           fmt.Sprintf("plan-%s-%s", plan.Goal, step.ID),
		TaskStartedAt:    time.Now(),
	}

	res, err := s.dispatch.Dispatch(ctx, cfg)
	if err != nil {
		s.logger.Warn("scheduler: dispatch error",
			zap.String("step_id", step.ID),
			zap.Error(err),
		)
		return &StepResult{
			StepID:  step.ID,
			Status:  "failed",
			Failures: []string{err.Error()},
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
	if agent.IsTerminalError(res) {
		out.Status = "failed"
	} else if len(res.SubmittedArtifacts) == 0 && len(step.ExpectedOutputs) > 0 {
		// Sub-agent returned without producing required artifacts — count
		// as failure even if Terminal looks "completed".
		out.Status = "failed"
		out.Failures = append(out.Failures, "step produced no artifacts despite expected outputs")
	} else {
		out.Status = "success"
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

// finalize marks any remaining un-dispatched step as skipped — used when
// the scheduler bails out early (budget exceeded). Returns the same
// outcome for chaining.
func finalize(o *SchedulerOutcome, results map[string]*StepResult, plan *Plan, reason string) *SchedulerOutcome {
	for _, step := range plan.Steps {
		if _, done := results[step.ID]; done {
			continue
		}
		results[step.ID] = &StepResult{StepID: step.ID, Status: "skipped", Failures: []string{reason}}
		o.StepResults = append(o.StepResults, results[step.ID])
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
			},
		}
	case "failed":
		// First failure entry is the most informative; include all in
		// the developer-facing Error field for debugging.
		var errMsg string
		if len(res.Failures) > 0 {
			errMsg = res.Failures[0]
		}
		out <- types.EngineEvent{
			Type: types.EngineEventStepFailed,
			TaskDispatch: &types.TaskDispatch{
				TaskID:       step.ID,
				SubagentType: step.SubagentType,
				ErrorType:    "step_failed",
				Error:        errMsg,
				Reason:       strings.Join(res.Failures, "; "),
			},
		}
	case "skipped":
		// Skipped path is handled at Run-time before dispatch via
		// emitStepSkipped — but we still fall through here for
		// defensive double-firing.
		emitStepSkipped(out, step, strings.Join(res.Failures, "; "))
	}
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
