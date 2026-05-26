package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"harnessclaw-go/internal/engine/scheduler/dispatch"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/workspace"
)

// Config holds injectable dependencies for a Strategy.
type Config struct {
	Caps dispatch.Capabilities
}

// Strategy implements dispatch.Strategy for the "plan" kind.
// Phase 1: plan-agent writes task breakdown to plan.json.
// Phase 2: plan-executor-agent dispatches tasks via freelance and updates plan.json.
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

// Run executes the plan strategy:
//  1. plan-agent leaf → writes task breakdown to plan.json
//  2. plan-executor-agent leaf → reads plan.json, dispatches tasks, updates status
func (p *Strategy) Run(ctx context.Context, taskID types.TaskID, deps dispatch.Deps) (types.MetaRef, error) {
	task, err := deps.Reader.Get(ctx, taskID)
	if err != nil {
		return "", err
	}

	// ── Phase 1: plan-agent writes task breakdown ────────────────────────────
	planRes, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
		buildPlanAgentSpec(task.LeafSpec.Goal, task.SessionID))
	if err != nil {
		return "", err
	}
	if planRes.Status != "done" {
		return "", &dispatch.LeafFailedError{Reason: "plan-agent: " + planRes.Reason}
	}

	// Guard: verify plan.json has tasks (plan-agent may have failed silently).
	if p.cfg.Caps.RootDir != "" {
		if err := p.requireNonEmptyPlan(task.SessionID); err != nil {
			return "", &dispatch.LeafFailedError{Reason: err.Error()}
		}
	}

	// ── Phase 2: plan-executor-agent executes the plan ───────────────────────
	execRes, err := dispatch.SpawnAndWaitOne(ctx, deps.Bus, taskID,
		buildPlanExecutorAgentSpec(task.LeafSpec.Goal, task.SessionID))
	if err != nil {
		return "", err
	}
	if execRes.Status != "done" {
		return "", &dispatch.LeafFailedError{Reason: "plan-executor-agent: " + execRes.Reason}
	}

	return types.MetaRef(execRes.OutputFile), nil
}

// requireNonEmptyPlan returns an error if plan.json exists but has no tasks.
func (p *Strategy) requireNonEmptyPlan(sessionID string) error {
	planPath := workspace.PlanPath(p.cfg.Caps.RootDir, sessionID)
	b, err := os.ReadFile(planPath)
	if err != nil {
		return nil // file not found is not a guard violation
	}
	var plan workspace.Plan
	if json.Unmarshal(b, &plan) != nil {
		return nil // parse error is not a guard violation
	}
	if len(plan.Tasks) == 0 {
		return fmt.Errorf("plan-agent wrote no tasks")
	}
	return nil
}
