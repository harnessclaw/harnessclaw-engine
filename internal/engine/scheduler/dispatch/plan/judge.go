package plan

import (
	"errors"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/spec"
)

const maxPlanSteps = 20

// PlanJudge implements rule-based plan validation from coordinator_judge.go.
type PlanJudge struct{}

// NewPlanJudge creates a new PlanJudge.
func NewPlanJudge() *PlanJudge { return &PlanJudge{} }

// ReviewPlan validates a plan goal and steps, ensuring it is non-empty and
// within resource constraints.
func (j *PlanJudge) ReviewPlan(goal string, steps []spec.TaskSpec) error {
	if goal == "" {
		return errors.New("plan has empty goal")
	}
	if len(steps) == 0 {
		return errors.New("plan has no steps")
	}
	if len(steps) > maxPlanSteps {
		return fmt.Errorf("plan has %d steps (max %d)", len(steps), maxPlanSteps)
	}
	for i, s := range steps {
		if s.Goal == "" {
			return fmt.Errorf("step %d has empty goal", i)
		}
	}
	return nil
}
