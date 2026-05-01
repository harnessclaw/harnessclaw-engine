package orchestrate

import (
	"strings"
	"testing"
)

func TestParsePlan_FencedJSON(t *testing.T) {
	raw := "<summary>拆成 2 步</summary>\n\n```json\n{\"steps\": [" +
		"{\"step_id\":\"step1\",\"subagent_type\":\"researcher\",\"task\":\"查竞品\",\"depends_on\":[]}," +
		"{\"step_id\":\"step2\",\"subagent_type\":\"writer\",\"task\":\"写报告\",\"depends_on\":[\"step1\"]}" +
		"]}\n```"

	plan, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[1].DependsOn[0] != "step1" {
		t.Errorf("step2 should depend on step1, got %v", plan.Steps[1].DependsOn)
	}
}

func TestParsePlan_BareJSON(t *testing.T) {
	raw := `<summary>计划</summary>
{"steps":[{"step_id":"s1","subagent_type":"writer","task":"写邮件","depends_on":[]}]}`

	plan, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(plan.Steps))
	}
}

func TestParsePlan_NoJSON(t *testing.T) {
	raw := "我没法拆解这个任务，太抽象了。"
	if _, err := ParsePlan(raw); err == nil {
		t.Error("expected error for missing JSON, got nil")
	}
}

func TestParsePlan_MalformedJSON(t *testing.T) {
	raw := "```json\n{not valid json}\n```"
	if _, err := ParsePlan(raw); err == nil {
		t.Error("expected unmarshal error, got nil")
	}
}

func TestPlanValidate_HappyPath(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "researcher", Task: "查", DependsOn: nil},
		{StepID: "s2", SubagentType: "writer", Task: "写", DependsOn: []string{"s1"}},
	}}
	allowed := map[string]bool{"researcher": true, "writer": true}
	if err := plan.Validate(allowed); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPlanValidate_EmptyPlan(t *testing.T) {
	plan := &Plan{}
	if err := plan.Validate(nil); err == nil {
		t.Error("expected error for empty plan")
	}
}

func TestPlanValidate_TooManySteps(t *testing.T) {
	steps := make([]Step, MaxSteps+1)
	for i := range steps {
		steps[i] = Step{
			StepID:       "s" + itoa(i),
			SubagentType: "worker",
			Task:         "x",
		}
	}
	plan := &Plan{Steps: steps}
	if err := plan.Validate(nil); err == nil {
		t.Error("expected error for too many steps")
	}
}

func TestPlanValidate_DuplicateStepID(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "worker", Task: "a"},
		{StepID: "s1", SubagentType: "worker", Task: "b"},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate error, got %v", err)
	}
}

func TestPlanValidate_UnknownSubagent(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "ghostwriter", Task: "x"},
	}}
	allowed := map[string]bool{"writer": true}
	if err := plan.Validate(allowed); err == nil || !strings.Contains(err.Error(), "ghostwriter") {
		t.Errorf("expected unknown subagent_type error, got %v", err)
	}
}

func TestPlanValidate_SelfDependency(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "worker", Task: "x", DependsOn: []string{"s1"}},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Errorf("expected self-dependency error, got %v", err)
	}
}

func TestPlanValidate_UnknownDep(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "worker", Task: "x", DependsOn: []string{"s99"}},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("expected unknown dep error, got %v", err)
	}
}

func TestPlanValidate_Cycle(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "worker", Task: "x", DependsOn: []string{"s2"}},
		{StepID: "s2", SubagentType: "worker", Task: "y", DependsOn: []string{"s3"}},
		{StepID: "s3", SubagentType: "worker", Task: "z", DependsOn: []string{"s1"}},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestPlanValidate_MissingTask(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "worker", Task: "  "},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "task") {
		t.Errorf("expected task-required error, got %v", err)
	}
}

func TestPlanValidate_MissingSubagentType(t *testing.T) {
	plan := &Plan{Steps: []Step{
		{StepID: "s1", Task: "x"},
	}}
	if err := plan.Validate(nil); err == nil || !strings.Contains(err.Error(), "subagent_type") {
		t.Errorf("expected subagent_type-required error, got %v", err)
	}
}

// itoa is a tiny standalone int→string helper for table tests so we don't
// import strconv across many test files.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
