package plan_test

import (
	"testing"

	schedulerplan "harnessclaw-go/internal/engine/scheduler/dispatch/plan"
	"harnessclaw-go/internal/engine/scheduler/spec"
)

func TestPlanJudge_ValidPlan(t *testing.T) {
	j := schedulerplan.NewPlanJudge()
	steps := []spec.TaskSpec{{Goal: "step 1"}, {Goal: "step 2"}}
	if err := j.ReviewPlan("overall goal", steps); err != nil {
		t.Fatalf("valid plan should pass: %v", err)
	}
}

func TestPlanJudge_EmptyGoal(t *testing.T) {
	j := schedulerplan.NewPlanJudge()
	if err := j.ReviewPlan("", []spec.TaskSpec{{Goal: "step 1"}}); err == nil {
		t.Fatal("empty plan goal should fail")
	}
}

func TestPlanJudge_NoSteps(t *testing.T) {
	j := schedulerplan.NewPlanJudge()
	if err := j.ReviewPlan("do something", nil); err == nil {
		t.Fatal("plan with no steps should fail")
	}
}

func TestPlanJudge_TooManySteps(t *testing.T) {
	j := schedulerplan.NewPlanJudge()
	steps := make([]spec.TaskSpec, 21)
	for i := range steps {
		steps[i] = spec.TaskSpec{Goal: "step"}
	}
	if err := j.ReviewPlan("goal", steps); err == nil {
		t.Fatal("plan with >20 steps should fail")
	}
}
