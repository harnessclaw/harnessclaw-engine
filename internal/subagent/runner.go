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
//
// Phase 1 skeleton: skips the actual LLM loop—writes a one-line summary
// of the goal + meta.json and returns. Real LLM↔tool loop will be added
// when migrating coordinator_react.go in phase 2.2.
func (r *Runner) Run(ctx context.Context) (types.MetaRef, error) {
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
