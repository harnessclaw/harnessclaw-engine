package engine

import (
	"context"
	"strings"
	"testing"

	"harnessclaw-go/pkg/types"
)

func TestJudge_ReviewPlan_HappyPath(t *testing.T) {
	j := NewJudge(nil)
	v := j.ReviewPlan(&Plan{
		Goal: "do the thing",
		Steps: []*PlanStep{{ID: "s1", SubagentType: "writer"}},
	})
	if !v.Pass {
		t.Errorf("valid plan rejected: %v", v.Reasons)
	}
	if v.Tier != "rule" {
		t.Errorf("plan judge should run at rule tier; got %q", v.Tier)
	}
}

func TestJudge_ReviewPlan_RejectsCycle(t *testing.T) {
	j := NewJudge(nil)
	v := j.ReviewPlan(&Plan{
		Goal: "broken",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer", DependsOn: []string{"s2"}},
			{ID: "s2", SubagentType: "writer", DependsOn: []string{"s1"}},
		},
	})
	if v.Pass {
		t.Error("cyclic plan should fail Judge")
	}
}

func TestJudge_ReviewPlan_RejectsBloatedPlan(t *testing.T) {
	j := NewJudge(nil)
	steps := make([]*PlanStep, 25)
	for i := range steps {
		steps[i] = &PlanStep{ID: idForN(i), SubagentType: "writer"}
	}
	v := j.ReviewPlan(&Plan{Goal: "do stuff", Steps: steps})
	if v.Pass {
		t.Error("25-step plan should be flagged as over-decomposed")
	}
	if len(v.Reasons) == 0 || !strings.Contains(v.Reasons[0], "too long") {
		t.Errorf("over-decomposition reason missing; got %v", v.Reasons)
	}
}

func TestJudge_ReviewPlan_FlagsMissingGoal(t *testing.T) {
	j := NewJudge(nil)
	v := j.ReviewPlan(&Plan{Steps: []*PlanStep{{ID: "s1", SubagentType: "writer"}}})
	if v.Pass {
		t.Error("plan without Goal should fail")
	}
}

func TestJudge_ReviewStep_HappyPath(t *testing.T) {
	j := NewJudge(nil)
	step := &PlanStep{
		ID:    "s1",
		SubagentType: "writer",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "draft_email", Required: true},
		},
	}
	result := &StepResult{
		StepID:    "s1",
		Status:    "success",
		Artifacts: []types.ArtifactRef{{ArtifactID: "art_1", Role: "draft_email"}},
	}
	v := j.ReviewStep(step, result)
	if !v.Pass {
		t.Errorf("step that fulfilled contract was rejected: %v", v.Reasons)
	}
}

func TestJudge_ReviewStep_FlagsMissingArtifact(t *testing.T) {
	j := NewJudge(nil)
	step := &PlanStep{
		ID:    "s1",
		SubagentType: "writer",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "draft_email", Required: true},
		},
	}
	result := &StepResult{StepID: "s1", Status: "success"}
	v := j.ReviewStep(step, result)
	if v.Pass {
		t.Error("step missing required artifact should fail")
	}
	if !strings.Contains(strings.Join(v.Reasons, " "), "artifacts") {
		t.Errorf("reason should mention missing artifacts; got %v", v.Reasons)
	}
}

func TestJudge_ReviewStep_FlagsWrongRole(t *testing.T) {
	j := NewJudge(nil)
	step := &PlanStep{
		ID:    "s1",
		SubagentType: "writer",
		ExpectedOutputs: []types.ExpectedOutput{
			{Role: "final_email", Required: true},
		},
	}
	result := &StepResult{
		StepID:    "s1",
		Status:    "success",
		Artifacts: []types.ArtifactRef{{ArtifactID: "art_1", Role: "draft_email"}},
	}
	v := j.ReviewStep(step, result)
	if v.Pass {
		t.Error("step with wrong artifact role should fail")
	}
}

func TestJudge_ReviewStep_FlagsFailedStatus(t *testing.T) {
	j := NewJudge(nil)
	step := &PlanStep{ID: "s1", SubagentType: "writer"}
	result := &StepResult{StepID: "s1", Status: "failed"}
	v := j.ReviewStep(step, result)
	if v.Pass {
		t.Error("status=failed should fail Judge")
	}
}

func TestJudge_ReviewGoal_HappyPath(t *testing.T) {
	j := NewJudge(nil)
	results := []*StepResult{
		{StepID: "s1", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "art_1"}}},
	}
	v := j.ReviewGoal(context.Background(), "write a report", results)
	if !v.Pass {
		t.Errorf("happy path goal review failed: %v", v.Reasons)
	}
	// Without a provider, ReviewGoal falls back to rule tier.
	if v.Tier != "rule" && v.Tier != "llm" {
		t.Errorf("goal review tier should be rule or llm; got %q", v.Tier)
	}
}

func TestJudge_ReviewGoal_PassesWhenAllStepsSucceedNoArtifacts(t *testing.T) {
	// File-based tasks write to disk without calling SubmitTaskResult — they
	// produce no artifact-store entries. ReviewGoal must not penalise this:
	// dispatchStep already marks a step failed when ExpectedOutputs declared
	// but not met, so the zero-artifact check here was a false negative.
	j := NewJudge(nil)
	results := []*StepResult{{StepID: "s1", Status: "success"}}
	v := j.ReviewGoal(context.Background(), "write a report", results)
	if !v.Pass {
		t.Errorf("all-success steps with no artifacts should pass ReviewGoal, got reasons: %v", v.Reasons)
	}
}

func TestJudge_ReviewGoal_FlagsAnyFailure(t *testing.T) {
	j := NewJudge(nil)
	results := []*StepResult{
		{StepID: "s1", Status: "success", Artifacts: []types.ArtifactRef{{ArtifactID: "a"}}},
		{StepID: "s2", Status: "failed"},
	}
	v := j.ReviewGoal(context.Background(), "compose", results)
	if v.Pass {
		t.Error("any step failure should fail goal review")
	}
}

// idForN is a stable ID generator for the bloated-plan test.
func idForN(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return "s0" + string(digits[n])
	}
	return "s" + string(digits[n/10]) + string(digits[n%10])
}
