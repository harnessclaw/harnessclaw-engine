package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// Plan execution status — returned to the Orchestrate tool aggregator.
const (
	StatusCompleted        = "completed"
	StatusPartialCompleted = "partial_completed"
	StatusFailed           = "failed"
)

// Per-step result statuses.
const (
	StepStatusCompleted = "completed"
	StepStatusFailed    = "failed"
	StepStatusSkipped   = "skipped"
	stepStatusPending   = "pending"
)

// StepResult is the per-step outcome reported back to the Orchestrate tool.
type StepResult struct {
	StepID       string              `json:"step_id"`
	SubagentType string              `json:"subagent_type"`
	Task         string              `json:"task"`
	Status       string              `json:"status"`
	Summary      string              `json:"summary,omitempty"`
	AgentID      string              `json:"agent_id,omitempty"`
	Attempts     int                 `json:"attempts"`
	Error        string              `json:"error,omitempty"`
	Deliverables []types.Deliverable `json:"deliverables,omitempty"`
}

// ExecuteResult is the aggregate outcome of running a Plan.
type ExecuteResult struct {
	Status       string              `json:"status"`
	Steps        []*StepResult       `json:"steps"`
	Deliverables []types.Deliverable `json:"deliverables,omitempty"`
}

// ExecuteOptions tunes a single Execute() call.
type ExecuteOptions struct {
	// ParentSessionID is forwarded to each spawned step so events stitch back
	// to the originating emma session.
	ParentSessionID string
	// ParentOut, when set, is forwarded so step subagent.start/end events
	// reach the WebSocket client.
	ParentOut chan<- types.EngineEvent
	// StepTimeout caps each individual SpawnSync call. Default: 5 minutes.
	StepTimeout time.Duration
	// MaxParallel caps how many step goroutines may run inside one wave.
	// Zero means unlimited (bounded by plan width, ≤ MaxSteps).
	MaxParallel int

	// --- Emit envelope context (v1.11+). When set, the executor emits
	// plan.* and task.dispatched/completed/failed/skipped events using
	// this trace context so observers can stitch the L2 graph into the
	// owning trace. Leave Sequencer nil to disable emit (events are
	// silently dropped). ---
	TraceID         string
	ParentEventID   string
	ParentTaskID    string
	PlanAgentID     string // L2 agent identity (e.g. "plan_agent")
	PlanAgentRunID  string
	Sequencer       *emit.Sequencer
	// PlanGoal is shown to the user in plan.created Display.Summary.
	PlanGoal string
}

// PlanExecutor runs a validated Plan as a parallel DAG of sub-agents.
type PlanExecutor struct {
	spawner agent.AgentSpawner
	logger  *zap.Logger
}

// NewPlanExecutor constructs an executor backed by the given AgentSpawner.
func NewPlanExecutor(spawner agent.AgentSpawner, logger *zap.Logger) *PlanExecutor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PlanExecutor{spawner: spawner, logger: logger}
}

// Execute runs the plan to completion (or partial completion if a step fails).
//
// Scheduling: steps with in-degree 0 are launched in parallel. As each step
// finishes, dependents whose remaining in-degree drops to 0 join the next
// wave. A failed step cascades a "skipped" status to all transitive
// dependents — those dependents are NOT spawned.
//
// Context propagation: each step's direct DependsOn summaries are joined
// into ContextSummary on the SpawnConfig. Per the layered-architecture doc,
// only summary-level info crosses agent boundaries; full output stays in the
// engine's TaskRegistry (populated by SpawnSync itself).
func (e *PlanExecutor) Execute(ctx context.Context, plan *Plan, opts ExecuteOptions) *ExecuteResult {
	if opts.StepTimeout <= 0 {
		opts.StepTimeout = 5 * time.Minute
	}

	planID := "plan_" + shortHash(plan)
	planStartedAt := time.Now()

	// Emit plan.created with the full task graph so the client can render
	// the multi-step plan as a parent task with children. Skipped if the
	// caller didn't wire an envelope context.
	e.emitPlanEvent(opts, types.EngineEventPlanCreated, planID, "created", "parallel", plan, &emit.Display{
		Title:      "计划已制定",
		Summary:    opts.PlanGoal,
		Icon:       emit.IconPlan,
		Visibility: emit.VisibilityDefault,
	}, nil)

	stepByID := make(map[string]*Step, len(plan.Steps))
	dependents := make(map[string][]string, len(plan.Steps))
	inDeg := make(map[string]int, len(plan.Steps))
	results := make(map[string]*StepResult, len(plan.Steps))
	order := make([]string, 0, len(plan.Steps))

	for i := range plan.Steps {
		s := &plan.Steps[i]
		stepByID[s.StepID] = s
		inDeg[s.StepID] = len(s.DependsOn)
		results[s.StepID] = &StepResult{
			StepID:       s.StepID,
			SubagentType: s.SubagentType,
			Task:         s.Task,
			Status:       stepStatusPending,
		}
		order = append(order, s.StepID)
		for _, dep := range s.DependsOn {
			dependents[dep] = append(dependents[dep], s.StepID)
		}
	}

	var resultsMu sync.Mutex

	// Loop one wave at a time. Each wave runs all currently-ready steps in
	// parallel; the next wave is computed from the wave's outcomes. Because
	// MaxSteps ≤ 10 the wave count is also tiny, so this is plenty fast.
	for {
		// Snapshot of ready set for this wave.
		ready := make([]string, 0)
		resultsMu.Lock()
		for _, id := range order {
			if results[id].Status == stepStatusPending && inDeg[id] == 0 {
				ready = append(ready, id)
			}
		}
		resultsMu.Unlock()

		if len(ready) == 0 {
			break
		}

		// Optionally bound parallelism within a wave.
		var sem chan struct{}
		if opts.MaxParallel > 0 {
			sem = make(chan struct{}, opts.MaxParallel)
		}

		var wg sync.WaitGroup
		for _, id := range ready {
			id := id
			wg.Add(1)
			if sem != nil {
				sem <- struct{}{}
			}
			go func() {
				defer wg.Done()
				if sem != nil {
					defer func() { <-sem }()
				}
				e.runStep(ctx, stepByID[id], results, &resultsMu, opts)
			}()
		}
		wg.Wait()

		// Update graph state from this wave's outcomes.
		resultsMu.Lock()
		for _, id := range ready {
			res := results[id]
			if res.Status == StepStatusCompleted {
				for _, depID := range dependents[id] {
					inDeg[depID]--
				}
			} else {
				// Failure cascades — mark all transitive descendants skipped
				// so they will not run in subsequent waves.
				skipped := e.cascadeSkip(id, dependents, results, "upstream step "+id+" failed")
				// Emit task.skipped for each cascade victim. Outside the
				// lock would be cleaner but the events themselves are
				// non-blocking sends, and we're already inside the wave
				// completion barrier.
				for _, sid := range skipped {
					e.emitStepEvent(opts, types.EngineEventStepSkipped, sid, &types.TaskDispatch{
						TaskID: sid,
						Reason: "upstream step " + id + " failed",
					}, &emit.Display{
						Title:      "跳过 " + sid,
						Summary:    "上游 " + id + " 失败",
						Visibility: emit.VisibilityCollapsed,
					}, nil, emit.SeverityWarn)
				}
			}
		}
		resultsMu.Unlock()
	}

	// Collect ordered results.
	out := &ExecuteResult{Steps: make([]*StepResult, 0, len(order))}
	completed, failed := 0, 0
	for _, id := range order {
		r := results[id]
		out.Steps = append(out.Steps, r)
		switch r.Status {
		case StepStatusCompleted:
			completed++
			out.Deliverables = append(out.Deliverables, r.Deliverables...)
		case StepStatusFailed, StepStatusSkipped:
			failed++
		case stepStatusPending:
			// Defensive: any leftover pending step is treated as failed.
			r.Status = StepStatusFailed
			r.Error = "step never became ready (graph stalled)"
			failed++
		}
	}

	switch {
	case failed == 0:
		out.Status = StatusCompleted
	case completed == 0:
		out.Status = StatusFailed
	default:
		out.Status = StatusPartialCompleted
	}

	// Plan terminator: emit plan.completed for any-success outcome,
	// plan.failed when the entire plan failed (no completed steps).
	// Both close the lifecycle pair opened by plan.created.
	planMetrics := &emit.Metrics{DurationMs: time.Since(planStartedAt).Milliseconds()}
	if out.Status == StatusFailed {
		// Surface the first failed step's error as the plan's error so
		// monitoring sees something useful. firstFailedError walks the
		// ordered step results to keep the choice deterministic.
		errType, errMsg := firstFailedError(out.Steps)
		e.emitPlanFailed(opts, planID, plan, &emit.Display{
			Title:      "计划失败",
			Summary:    fmt.Sprintf("0/%d 步骤成功", len(plan.Steps)),
			Icon:       emit.IconError,
			Visibility: emit.VisibilityDefault,
		}, planMetrics, errType, errMsg)
	} else {
		planTitle := "计划完成"
		planSeverity := emit.SeverityInfo
		if out.Status == StatusPartialCompleted {
			planTitle = "计划部分完成"
			planSeverity = emit.SeverityWarn
		}
		e.emitPlanEvent(opts, types.EngineEventPlanCompleted, planID, out.Status, "parallel", plan, &emit.Display{
			Title:      planTitle,
			Summary:    fmt.Sprintf("%d/%d 步骤成功", completed, len(plan.Steps)),
			Icon:       emit.IconPlan,
			Visibility: emit.VisibilityDefault,
		}, planMetrics, withSeverity(planSeverity))
	}

	return out
}

// firstFailedError walks the step results in declaration order and
// returns the first failed step's (ErrorType, message) pair. Used to
// give the plan-level failure event a concrete root cause.
func firstFailedError(steps []*StepResult) (emit.ErrorType, string) {
	for _, s := range steps {
		if s.Status == StepStatusFailed && s.Error != "" {
			return emit.ErrorTypeDependencyFail, s.Error
		}
	}
	return emit.ErrorTypeInternal, "all steps failed"
}

// emitPlanFailed sends the plan.failed lifecycle event. Distinct from
// the plan.completed path so the client can surface a different state
// (red banner vs check mark).
func (e *PlanExecutor) emitPlanFailed(
	opts ExecuteOptions,
	planID string,
	plan *Plan,
	display *emit.Display,
	metrics *emit.Metrics,
	errType emit.ErrorType,
	errMsg string,
) {
	if opts.ParentOut == nil || opts.Sequencer == nil {
		return
	}
	env := &emit.Envelope{
		EventID:       emit.NewEventID(),
		TraceID:       opts.TraceID,
		ParentEventID: opts.ParentEventID,
		TaskID:        planID,
		ParentTaskID:  opts.ParentTaskID,
		Seq:           opts.Sequencer.Next(opts.TraceID),
		Timestamp:     time.Now().UTC(),
		AgentRole:     emit.RoleOrchestrator,
		AgentID:       defaultStr(opts.PlanAgentID, "plan_agent"),
		AgentRunID:    opts.PlanAgentRunID,
		Severity:      emit.SeverityError,
	}
	opts.ParentOut <- types.EngineEvent{
		Type:     types.EngineEventPlanFailed,
		Envelope: env,
		Display:  display,
		Metrics:  metrics,
		TaskDispatch: &types.TaskDispatch{
			TaskID:    planID,
			ErrorType: string(errType),
			Error:     errMsg,
		},
	}
}

// withSeverity returns a small option function used by emitPlanEvent to
// customise the envelope's severity (info / warn / error).
func withSeverity(s emit.Severity) func(*emit.Envelope) {
	return func(env *emit.Envelope) { env.Severity = s }
}

// runStep spawns a single step's sub-agent, with up to MaxStepRetries retries
// on retryable failure. Mutates results[step.StepID] under resultsMu.
func (e *PlanExecutor) runStep(
	ctx context.Context,
	step *Step,
	results map[string]*StepResult,
	resultsMu *sync.Mutex,
	opts ExecuteOptions,
) {
	resultsMu.Lock()
	contextSummary := buildContextSummary(step.DependsOn, results)
	res := results[step.StepID]
	resultsMu.Unlock()

	stepStartedAt := time.Now()

	// step.dispatched goes out before the first attempt so the client can
	// show "派出 search_agent 查 2024 销量" while the spawn is still booting
	// (the wave queue may delay the actual start when MaxParallel < ready).
	e.emitStepEvent(opts, types.EngineEventStepDispatched, step.StepID, &types.TaskDispatch{
		TaskID:       step.StepID,
		SubagentType: step.SubagentType,
		InputSummary: truncate(step.Task, 200),
	}, &emit.Display{
		Title:      "派出 " + step.SubagentType,
		Summary:    truncate(step.Task, 120),
		Icon:       emit.IconDispatch,
		Visibility: emit.VisibilityDefault,
	}, nil, emit.SeverityInfo)

	// step.started signals the worker actually began execution — the
	// gap between dispatched and started is wave-queue latency.
	e.emitStepEvent(opts, types.EngineEventStepStarted, step.StepID, &types.TaskDispatch{
		TaskID:       step.StepID,
		SubagentType: step.SubagentType,
	}, &emit.Display{
		Title:      step.StepID + " 开始",
		Visibility: emit.VisibilityCollapsed,
	}, nil, emit.SeverityInfo)

	var lastErr error
	var lastSpawn *agent.SpawnResult
	for attempt := 1; attempt <= MaxStepRetries+1; attempt++ {
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
			res.Attempts = attempt - 1
			break
		default:
		}

		cfg := &agent.SpawnConfig{
			Prompt:          buildStepPrompt(step, attempt, lastErr, lastSpawn),
			AgentType:       tool.AgentTypeSync,
			SubagentType:    step.SubagentType,
			Description:     fmt.Sprintf("orchestrate %s", step.StepID),
			Name:            step.StepID,
			ContextSummary:  contextSummary,
			ParentSessionID: opts.ParentSessionID,
			ParentOut:       opts.ParentOut,
			Timeout:         opts.StepTimeout,
		}

		spawnRes, err := e.spawner.SpawnSync(ctx, cfg)
		lastErr = err
		lastSpawn = spawnRes

		resultsMu.Lock()
		res.Attempts = attempt
		resultsMu.Unlock()

		if err == nil && spawnRes != nil && spawnRes.Status == "completed" {
			resultsMu.Lock()
			res.Status = StepStatusCompleted
			res.Summary = spawnRes.Summary
			res.AgentID = spawnRes.AgentID
			res.Deliverables = spawnRes.Deliverables
			resultsMu.Unlock()
			e.logger.Info("orchestrate step completed",
				zap.String("step_id", step.StepID),
				zap.String("subagent_type", step.SubagentType),
				zap.Int("attempts", attempt),
			)
			// Emit task.completed so the client can mark this step done
			// without waiting for plan.completed.
			deliverablePaths := make([]string, 0, len(spawnRes.Deliverables))
			for _, d := range spawnRes.Deliverables {
				deliverablePaths = append(deliverablePaths, d.FilePath)
			}
			var stepUsage *emit.Metrics
			if spawnRes.Usage != nil {
				stepUsage = &emit.Metrics{
					DurationMs: time.Since(stepStartedAt).Milliseconds(),
					TokensIn:   spawnRes.Usage.InputTokens,
					TokensOut:  spawnRes.Usage.OutputTokens,
					CacheRead:  spawnRes.Usage.CacheRead,
					CacheWrite: spawnRes.Usage.CacheWrite,
				}
			} else {
				stepUsage = &emit.Metrics{DurationMs: time.Since(stepStartedAt).Milliseconds()}
			}
			e.emitStepEvent(opts, types.EngineEventStepCompleted, step.StepID, &types.TaskDispatch{
				TaskID:        step.StepID,
				SubagentType:  step.SubagentType,
				AgentID:       spawnRes.AgentID,
				OutputSummary: truncate(spawnRes.Summary, 200),
				Attempts:      attempt,
				Deliverables:  deliverablePaths,
			}, &emit.Display{
				Title:      "✓ " + step.StepID + " 完成",
				Summary:    truncate(spawnRes.Summary, 160),
				Icon:       emit.IconSuccess,
				Visibility: emit.VisibilityDefault,
			}, stepUsage, emit.SeverityInfo)
			return
		}

		e.logger.Warn("orchestrate step attempt failed",
			zap.String("step_id", step.StepID),
			zap.Int("attempt", attempt),
			zap.Error(err),
		)
	}

	resultsMu.Lock()
	res.Status = StepStatusFailed
	switch {
	case lastErr != nil:
		res.Error = lastErr.Error()
	case lastSpawn != nil:
		res.Error = fmt.Sprintf("step ended with status %q", lastSpawn.Status)
		res.Summary = lastSpawn.Summary
	default:
		res.Error = "spawner returned no result"
	}
	failureMsg := res.Error
	failureSummary := res.Summary
	resultsMu.Unlock()

	// step.failed: developer-facing message goes in Error; the persona
	// renders something user-friendly using its own voice rather than
	// repeating raw error text. We do not synthesize a Chinese fallback
	// here — let emma compose one.
	//
	// ErrorType classifies the failure for monitoring (orphan-timeout
	// when ctx was cancelled, otherwise generic dependency_failed).
	errType := emit.ErrorTypeDependencyFail
	if lastErr != nil && lastErr == context.Canceled {
		errType = emit.ErrorTypeAborted
	} else if lastErr != nil && lastErr == context.DeadlineExceeded {
		errType = emit.ErrorTypeOrphanTimeout
	}
	e.emitStepEvent(opts, types.EngineEventStepFailed, step.StepID, &types.TaskDispatch{
		TaskID:        step.StepID,
		SubagentType:  step.SubagentType,
		ErrorType:     string(errType),
		Error:         failureMsg,
		OutputSummary: truncate(failureSummary, 200),
		Attempts:      MaxStepRetries + 1,
		Retryable:     false,
	}, &emit.Display{
		Title:      "✗ " + step.StepID + " 失败",
		Summary:    truncate(failureMsg, 160),
		Icon:       emit.IconError,
		Visibility: emit.VisibilityDefault,
	}, &emit.Metrics{DurationMs: time.Since(stepStartedAt).Milliseconds()}, emit.SeverityError)
}

// cascadeSkip marks every transitive dependent of failedID as Skipped if they
// are still pending. Caller must hold resultsMu. Returns the IDs that were
// just transitioned from pending → skipped so the caller can emit per-task
// task.skipped events.
func (e *PlanExecutor) cascadeSkip(
	failedID string,
	dependents map[string][]string,
	results map[string]*StepResult,
	reason string,
) []string {
	queue := append([]string(nil), dependents[failedID]...)
	var skipped []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		r := results[cur]
		if r.Status != stepStatusPending {
			continue
		}
		r.Status = StepStatusSkipped
		r.Error = reason
		skipped = append(skipped, cur)
		queue = append(queue, dependents[cur]...)
	}
	return skipped
}

// buildContextSummary stitches direct dependency summaries into a multiline
// blob suitable for SpawnConfig.ContextSummary. Caller must hold resultsMu.
func buildContextSummary(deps []string, results map[string]*StepResult) string {
	if len(deps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# 前序步骤产出\n\n")
	for _, id := range deps {
		r, ok := results[id]
		if !ok {
			continue
		}
		summary := strings.TrimSpace(r.Summary)
		if summary == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s（%s）：%s\n", id, r.SubagentType, summary)
	}
	return b.String()
}

// buildStepPrompt assembles the step's task instruction. On retry it appends
// the previous failure reason so the spawned agent knows what to fix.
func buildStepPrompt(step *Step, attempt int, lastErr error, lastSpawn *agent.SpawnResult) string {
	if attempt == 1 {
		return step.Task
	}
	var b strings.Builder
	b.WriteString(step.Task)
	b.WriteString("\n\n# 上次失败原因\n")
	if lastErr != nil {
		fmt.Fprintf(&b, "上一次尝试出错：%s\n", lastErr.Error())
	}
	if lastSpawn != nil {
		fmt.Fprintf(&b, "上次结束状态：%s\n", lastSpawn.Status)
		if lastSpawn.Summary != "" {
			fmt.Fprintf(&b, "上次输出摘要：%s\n", lastSpawn.Summary)
		}
	}
	b.WriteString("请调整方案后再试一次。")
	return b.String()
}

// truncate clips s to n runes for safe inclusion in Display fields.
// The "…" suffix signals truncation. Returns "" when n <= 0.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// shortHash returns a short stable identifier derived from the plan's
// step IDs. We don't need cryptographic strength here — just a value
// distinct enough that two coexistent plans won't collide on screen.
func shortHash(plan *Plan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return "empty"
	}
	var b strings.Builder
	for _, s := range plan.Steps {
		b.WriteString(s.StepID)
		b.WriteByte('|')
	}
	// FNV-1a 32-bit hash, encoded as 8 hex chars.
	const fnvOffset, fnvPrime uint32 = 2166136261, 16777619
	h := fnvOffset
	for _, c := range b.String() {
		h ^= uint32(c)
		h *= fnvPrime
	}
	return fmt.Sprintf("%08x", h)
}

// emitPlanEvent dispatches a plan_* engine event to opts.ParentOut. No-op
// when the caller didn't supply a Sequencer or ParentOut, so the executor
// remains usable from contexts that don't care about emit (tests, batch
// runs).
func (e *PlanExecutor) emitPlanEvent(
	opts ExecuteOptions,
	eventType types.EngineEventType,
	planID string,
	status string,
	strategy string,
	plan *Plan,
	display *emit.Display,
	metrics *emit.Metrics,
	overrides ...func(*emit.Envelope),
) {
	if opts.ParentOut == nil || opts.Sequencer == nil {
		return
	}
	tasks := make([]types.PlanTaskInfo, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		tasks = append(tasks, types.PlanTaskInfo{
			TaskID:          s.StepID,
			SubagentType:    s.SubagentType,
			DependsOn:       s.DependsOn,
			UserFacingTitle: truncate(s.Task, 80),
		})
	}
	env := &emit.Envelope{
		EventID:       emit.NewEventID(),
		TraceID:       opts.TraceID,
		ParentEventID: opts.ParentEventID,
		TaskID:        planID,
		ParentTaskID:  opts.ParentTaskID,
		Seq:           opts.Sequencer.Next(opts.TraceID),
		Timestamp:     time.Now().UTC(),
		AgentRole:     emit.RoleOrchestrator,
		AgentID:       defaultStr(opts.PlanAgentID, "plan_agent"),
		AgentRunID:    opts.PlanAgentRunID,
		Severity:      emit.SeverityInfo,
	}
	for _, opt := range overrides {
		opt(env)
	}
	opts.ParentOut <- types.EngineEvent{
		Type:     eventType,
		Envelope: env,
		Display:  display,
		Metrics:  metrics,
		PlanEvent: &types.PlanEvent{
			PlanID:   planID,
			Goal:     opts.PlanGoal,
			Strategy: strategy,
			Status:   status,
			Tasks:    tasks,
		},
	}
}

// emitStepEvent dispatches a step_* engine event to opts.ParentOut. The
// stepID is propagated as the envelope.TaskID and the parent_task_id is
// the plan's task ID (so clients can group steps under their plan card).
func (e *PlanExecutor) emitStepEvent(
	opts ExecuteOptions,
	eventType types.EngineEventType,
	taskID string,
	dispatch *types.TaskDispatch,
	display *emit.Display,
	metrics *emit.Metrics,
	severity emit.Severity,
) {
	if opts.ParentOut == nil || opts.Sequencer == nil {
		return
	}
	env := &emit.Envelope{
		EventID:       emit.NewEventID(),
		TraceID:       opts.TraceID,
		ParentEventID: opts.ParentEventID,
		TaskID:        taskID,
		ParentTaskID:  opts.ParentTaskID,
		Seq:           opts.Sequencer.Next(opts.TraceID),
		Timestamp:     time.Now().UTC(),
		AgentRole:     emit.RoleOrchestrator,
		AgentID:       defaultStr(opts.PlanAgentID, "plan_agent"),
		AgentRunID:    opts.PlanAgentRunID,
		Severity:      severity,
	}
	opts.ParentOut <- types.EngineEvent{
		Type:         eventType,
		Envelope:     env,
		Display:      display,
		Metrics:      metrics,
		TaskDispatch: dispatch,
	}
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
