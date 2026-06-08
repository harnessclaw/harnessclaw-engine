package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"harnessclaw-go/internal/engine/agent/scheduler/dispatch"
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/legacy/workspace"
	pkgtypes "harnessclaw-go/pkg/types"
)

// Config holds injectable dependencies for a Strategy.
type Config struct {
	Caps dispatch.Capabilities
}

// Strategy implements dispatch.Strategy for the "plan" kind.
//
// Two-phase orchestration:
//   - Phase 1: spawn plan_agent (L3) → writes plan.json with task breakdown.
//   - Phase 2: L2 iterates plan.json in topological order, spawning one
//     sub-agent per step via SpawnAndWaitOne. Applies Budget, cascadeSkip on
//     failure, and emits plan/step events to the optional Emitter sink.
//
// On run completion, plan.json is rewritten with final per-task status so the
// caller / promoteToDeliverables can read the up-to-date snapshot.
type Strategy struct{ cfg Config }

// NewWithConfig creates a Strategy with the given Config.
func NewWithConfig(cfg Config) *Strategy {
	if cfg.Caps.LeafKind == "" {
		cfg.Caps.LeafKind = "react-leaf"
	}
	cfg.Caps.AllowSubmit = true
	return &Strategy{cfg: cfg}
}

// New creates a Strategy with the given Capabilities.
func New(caps dispatch.Capabilities) *Strategy {
	return NewWithConfig(Config{Caps: caps})
}

func (Strategy) Kind() types.Kind                       { return types.KindPlan }
func (p *Strategy) Capabilities() dispatch.Capabilities { return p.cfg.Caps }

// Run executes the plan strategy. See package docs for the two-phase flow.
//
// MetaRef returned points at the plan.json relative path
// ({sessionRoot}/plan.json → "plan.json"). On any irrecoverable error, the
// MetaRef is "" and the error describes the cause; partial progress is
// preserved in plan.json so callers can inspect it.
func (p *Strategy) Run(ctx context.Context, taskID types.TaskID, deps dispatch.Deps) (types.MetaRef, error) {
	task, err := deps.Reader.Get(ctx, taskID)
	if err != nil {
		return "", err
	}
	sessionID := task.SessionID
	emit := nonNilEmitter(deps.Emitter)

	// ── Phase 1: plan_agent writes plan.json ────────────────────────────────
	planRes, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
		buildPlanAgentSpec(task.LeafSpec.Goal, sessionID))
	if err != nil {
		return "", err
	}
	if planRes.Status != msgbus.ResultStatusDone {
		return "", &dispatch.LeafFailedError{Reason: "plan_agent: " + planRes.Reason}
	}

	// Backward-compat: when RootDir is unset (test/skeleton mode) the strategy
	// has nowhere to read plan.json from. We accept Phase-1's MetaRef as the
	// final result rather than failing — this matches the pre-parity behavior
	// of the legacy plan strategy.
	if p.cfg.Caps.RootDir == "" {
		return types.MetaRef(planRes.OutputFile), nil
	}

	plan, err := p.loadPlan(sessionID)
	if err != nil {
		if os.IsNotExist(err) {
			// plan_agent did not materialise a plan.json — treat as degenerate
			// empty plan. Coordinator/legacy callers relying on the stub
			// behavior keep working; new callers wiring plan_agent to write
			// plan.json get the full Phase-2 iteration.
			return types.MetaRef(planRes.OutputFile), nil
		}
		return "", &dispatch.LeafFailedError{Reason: "load plan.json: " + err.Error()}
	}
	if len(plan.Tasks) == 0 {
		return types.MetaRef("plan.json"), nil
	}
	order, terr := TopoOrder(plan)
	if terr != nil {
		return "", &dispatch.LeafFailedError{Reason: terr.Error()}
	}

	emit.Emit(pkgtypes.EngineEvent{
		Type: pkgtypes.EngineEventPlanCreated,
		PlanEvent: &pkgtypes.PlanEvent{
			PlanID:   sessionID,
			Goal:     task.LeafSpec.Goal,
			Strategy: "sequential",
			Status:   "created",
			Tasks:    planTaskInfos(plan, order),
		},
	})

	// ── Phase 2: L2 iterates plan.json ──────────────────────────────────────
	tracker := NewBudgetTracker(Budget{
		MaxSteps:    p.cfg.Caps.PlanMaxSteps,
		MaxFailures: p.cfg.Caps.PlanMaxFailures,
	})

	var planFailed bool
	var failReason string

	for _, stepID := range order {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		t := plan.Tasks[stepID]
		if t == nil {
			continue
		}
		// Already terminal (e.g., cancelled by an earlier cascadeSkip).
		if t.Status != workspace.StatusPending {
			if t.Status == workspace.StatusCancelled {
				emit.Emit(stepEvent(pkgtypes.EngineEventStepSkipped, sessionID, stepID, t, "dependency_failed"))
			}
			continue
		}
		// Budget check before any work.
		if reason := tracker.Exceeded(); reason != "" {
			planFailed, failReason = true, reason
			// Mark all remaining pending tasks as cancelled.
			for _, sid := range order {
				if pt := plan.Tasks[sid]; pt != nil && pt.Status == workspace.StatusPending {
					pt.Status = workspace.StatusCancelled
					emit.Emit(stepEvent(pkgtypes.EngineEventStepSkipped, sessionID, sid, pt, "budget_exceeded"))
				}
			}
			break
		}

		// Dispatch the step.
		tracker.ConsumeStep()
		t.Status = workspace.StatusRunning
		t.StartedAt = time.Now()
		t.Attempt++
		emit.Emit(stepEvent(pkgtypes.EngineEventStepStarted, sessionID, stepID, t, ""))

		res, derr := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
			buildStepSpec(stepID, t, sessionID))
		t.EndedAt = time.Now()
		if derr != nil {
			t.Status = workspace.StatusFailed
			emit.Emit(stepEvent(pkgtypes.EngineEventStepFailed, sessionID, stepID, t, derr.Error()))
			skipped := CascadeSkip(plan, stepID)
			for _, sid := range skipped {
				emit.Emit(stepEvent(pkgtypes.EngineEventStepSkipped, sessionID, sid, plan.Tasks[sid], "dependency_failed"))
			}
			if reason := tracker.RecordFailure(); reason != "" {
				planFailed, failReason = true, reason
				break
			}
			continue
		}

		switch res.Status {
		case msgbus.ResultStatusDone:
			t.Status = workspace.StatusDone
			t.SummaryRef = res.Summary
			if res.OutputFile != "" && t.SummaryRef == "" {
				t.SummaryRef = res.OutputFile
			}
			emit.Emit(stepEvent(pkgtypes.EngineEventStepCompleted, sessionID, stepID, t, ""))

		case msgbus.ResultStatusCancelled:
			t.Status = workspace.StatusCancelled
			emit.Emit(stepEvent(pkgtypes.EngineEventStepSkipped, sessionID, stepID, t, "cancelled"))

		default:
			t.Status = workspace.StatusFailed
			emit.Emit(stepEvent(pkgtypes.EngineEventStepFailed, sessionID, stepID, t, res.Reason))
			skipped := CascadeSkip(plan, stepID)
			for _, sid := range skipped {
				emit.Emit(stepEvent(pkgtypes.EngineEventStepSkipped, sessionID, sid, plan.Tasks[sid], "dependency_failed"))
			}
			if reason := tracker.RecordFailure(); reason != "" {
				planFailed, failReason = true, reason
			}
		}

		// Persist plan.json after every step so a crash leaves an
		// inspectable partial snapshot.
		if werr := p.writePlan(sessionID, plan); werr != nil {
			return "", fmt.Errorf("plan: persist plan.json: %w", werr)
		}
		if planFailed {
			break
		}
	}

	// Final emit + final persist.
	if werr := p.writePlan(sessionID, plan); werr != nil {
		return "", fmt.Errorf("plan: persist plan.json: %w", werr)
	}

	snapshot := tracker.Snapshot()
	if planFailed {
		emit.Emit(pkgtypes.EngineEvent{
			Type: pkgtypes.EngineEventPlanFailed,
			PlanEvent: &pkgtypes.PlanEvent{
				PlanID:   sessionID,
				Goal:     task.LeafSpec.Goal,
				Strategy: "sequential",
				Status:   "failed",
			},
		})
		return types.MetaRef("plan.json"),
			&dispatch.LeafFailedError{Reason: fmt.Sprintf("plan aborted: %s (steps=%d failures=%d elapsed=%s)",
				failReason, snapshot.Steps, snapshot.Failures, snapshot.Elapsed)}
	}

	emit.Emit(pkgtypes.EngineEvent{
		Type: pkgtypes.EngineEventPlanCompleted,
		PlanEvent: &pkgtypes.PlanEvent{
			PlanID:   sessionID,
			Goal:     task.LeafSpec.Goal,
			Strategy: "sequential",
			Status:   "completed",
		},
	})
	return types.MetaRef("plan.json"), nil
}

// loadPlan reads {sessionRoot}/plan.json into a workspace.Plan.
func (p *Strategy) loadPlan(sessionID string) (*workspace.Plan, error) {
	if p.cfg.Caps.RootDir == "" {
		return nil, fmt.Errorf("plan: RootDir is empty (caps misconfigured)")
	}
	path := workspace.PlanPath(p.cfg.Caps.RootDir, sessionID)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pl workspace.Plan
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, fmt.Errorf("parse plan.json: %w", err)
	}
	return &pl, nil
}

// writePlan persists the plan back to {sessionRoot}/plan.json atomically.
func (p *Strategy) writePlan(sessionID string, pl *workspace.Plan) error {
	if p.cfg.Caps.RootDir == "" {
		return fmt.Errorf("plan: RootDir is empty")
	}
	pl.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(pl, "", "  ")
	if err != nil {
		return err
	}
	path := workspace.PlanPath(p.cfg.Caps.RootDir, sessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// buildStepSpec builds the TaskSpec dispatched for one plan step.
func buildStepSpec(stepID string, t *workspace.Task, sessionID string) spec.TaskSpec {
	return spec.TaskSpec{
		LocalID:      "step-" + stepID,
		Goal:         t.Title,
		Hint:         spec.Hint{Kind: types.KindLeaf},
		Layout:       "per-task",
		SessionID:    sessionID,
		SubagentType: t.Agent,
		InputPaths:   append([]string(nil), t.InputPaths...),
	}
}

// stepEvent wraps a per-step state change into an EngineEvent with the
// TaskDispatch payload populated.
func stepEvent(kind pkgtypes.EngineEventType, sessionID, stepID string, t *workspace.Task, reason string) pkgtypes.EngineEvent {
	_ = sessionID // sessionID is the plan_id surface, carried via outer PlanEvent; per-step payload doesn't repeat it.
	evt := pkgtypes.EngineEvent{
		Type:         kind,
		ParentStepID: stepID,
		TaskDispatch: &pkgtypes.TaskDispatch{
			TaskID: stepID,
		},
	}
	if t != nil {
		evt.TaskDispatch.SubagentType = t.Agent
		evt.TaskDispatch.InputSummary = t.Title
		evt.TaskDispatch.Attempts = t.Attempt
		evt.TaskDispatch.OutputSummary = t.SummaryRef
	}
	if reason != "" {
		evt.TaskDispatch.Reason = reason
		evt.TaskDispatch.Error = reason
	}
	return evt
}

// planTaskInfos builds the PlanTaskInfo slice in dispatch order for the
// plan_created event.
func planTaskInfos(plan *workspace.Plan, order []string) []pkgtypes.PlanTaskInfo {
	if plan == nil {
		return nil
	}
	out := make([]pkgtypes.PlanTaskInfo, 0, len(order))
	for _, id := range order {
		t := plan.Tasks[id]
		if t == nil {
			continue
		}
		out = append(out, pkgtypes.PlanTaskInfo{
			TaskID:          id,
			SubagentType:    t.Agent,
			DependsOn:       append([]string(nil), t.DependsOn...),
			UserFacingTitle: t.Title,
		})
	}
	return out
}

// nonNilEmitter returns the provided emitter, or a no-op when nil so call
// sites can avoid nil checks.
func nonNilEmitter(e dispatch.Emitter) dispatch.Emitter {
	if e != nil {
		return e
	}
	return dispatch.EmitterFunc(nil)
}
