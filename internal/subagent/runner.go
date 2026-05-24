package subagent

import (
	"context"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/workspace"
)

// Runner runs the LLM ↔ tool inner loop for one sub-agent.
type Runner struct {
	ctx        LeafContext
	consumerID string
}

// NewRunner creates a Runner for the given LeafContext.
func NewRunner(ctx LeafContext, consumerID string) *Runner {
	return &Runner{ctx: ctx, consumerID: consumerID}
}

// Run executes the leaf and returns the MetaRef pointing to meta.json.
// If SpawnFn is set, it delegates to the real sub-agent spawner; otherwise
// it falls back to the phase-1 stub (writes a summary file and meta.json).
func (r *Runner) Run(ctx context.Context) (types.MetaRef, error) {
	if r.ctx.SpawnFn != nil {
		return r.runWithSpawnFn(ctx)
	}
	return r.runStub(ctx)
}

// runWithSpawnFn calls SpawnFn and writes meta.json from the result.
func (r *Runner) runWithSpawnFn(ctx context.Context) (types.MetaRef, error) {
	result, err := r.ctx.SpawnFn(ctx)
	if err != nil {
		return "", err
	}

	summary := r.ctx.SpecRef.Goal
	if result != nil && result.Output != "" {
		summary = result.Output
		// cap to MaxSummaryRunes to satisfy workspace.Meta.Validate
		runes := []rune(summary)
		if len(runes) > workspace.MaxSummaryRunes {
			summary = string(runes[:workspace.MaxSummaryRunes])
		}
	}

	rel, err := r.ctx.Workspace.WriteMeta(ctx, workspace.Meta{
		TaskID:  string(r.ctx.TaskID),
		Agent:   r.consumerID,
		Status:  workspace.StatusDone,
		Summary: summary,
	})
	if err != nil {
		return "", err
	}
	return types.MetaRef(rel), nil
}

// runStub is the phase-1 skeleton: writes a one-line summary of the goal
// + meta.json and returns. Real LLM↔tool loop will be added when migrating
// coordinator_react.go in phase 2.2.
func (r *Runner) runStub(ctx context.Context) (types.MetaRef, error) {
	dir := r.ctx.Workspace.TaskDir()
	_ = dir // referenced for future use

	body := fmt.Sprintf("Goal: %s\nLeaf %s done by %s\n",
		r.ctx.SpecRef.Goal, r.ctx.TaskID, r.consumerID)
	if err := r.ctx.Workspace.WriteFile(ctx, "summary.md", []byte(body)); err != nil {
		return "", err
	}

	rel, err := r.ctx.Workspace.WriteMeta(ctx, workspace.Meta{
		TaskID:  string(r.ctx.TaskID),
		Agent:   r.consumerID,
		Status:  workspace.StatusDone,
		Summary: r.ctx.SpecRef.Goal,
		Outputs: []workspace.Output{{Path: "summary.md"}},
	})
	if err != nil {
		return "", err
	}
	return types.MetaRef(rel), nil
}
