package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// fakePlannerProvider is a minimal provider.Provider that returns
// caller-supplied stream events. Each test stages a sequence of attempts:
// attempts[0] handles the first call, attempts[1] the first retry, etc.
// When the planner exhausts the staged attempts the test fake panics —
// surfacing test bugs (planner retried unexpectedly) rather than
// hanging.
type fakePlannerProvider struct {
	attempts []func() ([]types.StreamEvent, error)
	calls    int
	lastReq  *provider.ChatRequest
}

func (f *fakePlannerProvider) Chat(_ context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	f.lastReq = req
	if f.calls >= len(f.attempts) {
		// Test bug: planner called Chat more than the staged attempts.
		// Returning an error makes the planner give up rather than
		// loop forever on an unstaged code path.
		return nil, errors.New("fakePlannerProvider: no more staged attempts")
	}
	stage := f.attempts[f.calls]
	f.calls++
	events, err := stage()
	if err != nil {
		return nil, err
	}
	ch := make(chan types.StreamEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &provider.ChatStream{
		Events: ch,
		Err:    func() error { return nil },
	}, nil
}

func (f *fakePlannerProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}
func (f *fakePlannerProvider) Name() string { return "fake" }

// emitPlanStream is a convenience that turns a JSON string into a stream
// containing a single tool_use event for the emit_plan tool.
func emitPlanStream(toolInputJSON string) func() ([]types.StreamEvent, error) {
	return func() ([]types.StreamEvent, error) {
		return []types.StreamEvent{
			{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{
					ID:    "tu_planner_1",
					Name:  plannerEmitToolName,
					Input: toolInputJSON,
				},
			},
			{Type: types.StreamEventMessageEnd, StopReason: "tool_use"},
		}, nil
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func newPlannerForTest(prov provider.Provider) *LLMPlanner {
	return NewLLMPlanner(prov, "test-model").WithMaxAttempts(3)
}

// ----- Validation gate tests (no provider call expected) -----

func TestLLMPlanner_RejectsEmptyGoal(t *testing.T) {
	prov := &fakePlannerProvider{}
	if _, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{}); err == nil {
		t.Fatal("expected error on empty goal")
	}
	if prov.calls != 0 {
		t.Errorf("provider should not be called on empty goal; got calls=%d", prov.calls)
	}
}

// LLMPlanner is roster-agnostic: empty AvailableSubagents is FINE; the
// planner doesn't use the field. Executor selection is a downstream
// concern (SubagentResolver) that operates with the current roster at
// dispatch time.
func TestLLMPlanner_AcceptsEmptyAvailableSubagents(t *testing.T) {
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "single step",
		"steps":     []map[string]any{{"id": "s1", "description": "do x", "prompt": "do x"}},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	_, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal: "anything",
		// AvailableSubagents intentionally empty — must not block planning
	})
	if err != nil {
		t.Fatalf("Plan should succeed without AvailableSubagents (planner is roster-agnostic): %v", err)
	}
}

// ----- Happy-path tests -----

func TestLLMPlanner_DispatchesValidPlan(t *testing.T) {
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "trivial single-step lookup",
		"steps": []map[string]any{
			{"id": "s1", "description": "translate the paragraph", "prompt": "Translate paragraph"},
		},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	out, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "Translate this paragraph",
		AvailableSubagents: []string{"writer"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(out.Plan.Steps) != 1 {
		t.Errorf("Steps len = %d, want 1 (model said single step)", len(out.Plan.Steps))
	}
	if out.Rationale != "trivial single-step lookup" {
		t.Errorf("Rationale = %q", out.Rationale)
	}
	if out.Plan.Goal != "Translate this paragraph" {
		t.Errorf("Goal stamping failed: %q", out.Plan.Goal)
	}
	if prov.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", prov.calls)
	}
}

// The planner's system prompt must NOT mention sub-agents, roles, or
// skills. It must focus on the WHAT (decomposition) and leave WHO
// (executor selection) to the runtime. AvailableSubagents on
// PlannerInput is kept for backward compat but ignored by the planner.
func TestLLMPlanner_PromptIsRosterAgnostic(t *testing.T) {
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "x",
		"steps":     []map[string]any{{"id": "s1", "description": "x", "prompt": "x"}},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	planner := newPlannerForTest(prov).WithMaxSteps(7)
	_, err := planner.Plan(context.Background(), PlannerInput{
		Goal:               "anything",
		AvailableSubagents: []string{"writer", "researcher", "analyst"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	sys := prov.lastReq.System
	for _, name := range []string{"writer", "researcher", "analyst"} {
		if strings.Contains(sys, name) {
			t.Errorf("system prompt LEAKED subagent name %q — planner must stay roster-agnostic.\nPrompt:\n%s",
				name, sys)
		}
	}
	if strings.Contains(sys, "subagent_type") {
		t.Errorf("system prompt should not mention subagent_type. Prompt:\n%s", sys)
	}
	if !strings.Contains(sys, "7") {
		t.Errorf("system prompt should mention maxSteps=7. Prompt:\n%s", sys)
	}
	tools := prov.lastReq.Tools
	if len(tools) != 1 || tools[0].Name != plannerEmitToolName {
		t.Errorf("expected exactly one emit_plan tool; got %+v", tools)
	}
}

// ----- Multi-step / parallel tests (the regression we care about) -----

func TestLLMPlanner_MultiStepDecompositionRespected(t *testing.T) {
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "research → analyze → design → draft → review",
		"steps": []map[string]any{
			{"id": "s1", "description": "research", "prompt": "research X"},
			{"id": "s2", "description": "analyze", "prompt": "analyze findings", "depends_on": []string{"s1"}},
			{"id": "s3", "description": "design", "prompt": "design Y", "depends_on": []string{"s2"}},
			{"id": "s4", "description": "draft", "prompt": "draft report", "depends_on": []string{"s3"}},
			{"id": "s5", "description": "review", "prompt": "review draft", "depends_on": []string{"s4"}},
		},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	out, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "调研 vLLM、分析其与 FlashAttention 的差异、设计架构、写报告、自审一遍",
		AvailableSubagents: []string{"writer", "researcher", "analyst"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := len(out.Plan.Steps); got != 5 {
		t.Fatalf("Steps len = %d, want 5 (multi-stage decomposition must NOT collapse to 2)", got)
	}
}

func TestLLMPlanner_ParallelStepsAllowed(t *testing.T) {
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "two independent fetches feed one summary",
		"steps": []map[string]any{
			{"id": "s1", "description": "fetch A", "prompt": "fetch A"},
			{"id": "s2", "description": "fetch B", "prompt": "fetch B"},
			{"id": "s3", "description": "summarize", "prompt": "summarize", "depends_on": []string{"s1", "s2"}},
		},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	out, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "并行抓 A 和 B 然后汇总",
		AvailableSubagents: []string{"researcher", "analyst"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if l := len(out.Plan.Steps); l != 3 {
		t.Fatalf("expected 3 steps, got %d", l)
	}
	if len(out.Plan.Steps[0].DependsOn) != 0 || len(out.Plan.Steps[1].DependsOn) != 0 {
		t.Errorf("first two steps should be parallel (no DependsOn)")
	}
}

// ----- Retry-on-validation-failure tests -----

func TestLLMPlanner_RetriesOnCycleDependency(t *testing.T) {
	bad := mustMarshal(t, map[string]any{
		"rationale": "broken",
		"steps": []map[string]any{
			{"id": "s1", "description": "x", "prompt": "x", "depends_on": []string{"s2"}},
			{"id": "s2", "description": "y", "prompt": "y", "depends_on": []string{"s1"}},
		},
	})
	good := mustMarshal(t, map[string]any{
		"rationale": "fixed",
		"steps": []map[string]any{
			{"id": "s1", "description": "x", "prompt": "x"},
			{"id": "s2", "description": "y", "prompt": "y", "depends_on": []string{"s1"}},
		},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){
			emitPlanStream(bad),
			emitPlanStream(good),
		},
	}
	out, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "anything",
		AvailableSubagents: []string{"researcher"},
	})
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if prov.calls != 2 {
		t.Errorf("expected 2 calls (one bad, one good); got %d", prov.calls)
	}
	if len(out.Plan.Steps) != 2 {
		t.Errorf("recovered plan should have 2 steps")
	}
}

func TestLLMPlanner_RetryFeedbackContainsValidationReason(t *testing.T) {
	bad := mustMarshal(t, map[string]any{
		"rationale": "broken",
		"steps":     []map[string]any{}, // empty — fails Validate
	})
	good := mustMarshal(t, map[string]any{
		"rationale": "x",
		"steps":     []map[string]any{{"id": "s1", "description": "x", "prompt": "x"}},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){
			emitPlanStream(bad),
			emitPlanStream(good),
		},
	}
	_, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "anything",
		AvailableSubagents: []string{"writer"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// The retry's user message must include the failure reason so the
	// model knows what to fix.
	user := prov.lastReq.Messages[0].Content[0].Text
	if !strings.Contains(user, "validation failed") {
		t.Errorf("retry user message should include validation reason; got:\n%s", user)
	}
}

func TestLLMPlanner_GivesUpAfterMaxAttempts(t *testing.T) {
	bad := mustMarshal(t, map[string]any{
		"rationale": "broken",
		"steps":     []map[string]any{},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){
			emitPlanStream(bad), emitPlanStream(bad), emitPlanStream(bad),
		},
	}
	planner := newPlannerForTest(prov).WithMaxAttempts(3)
	_, err := planner.Plan(context.Background(), PlannerInput{
		Goal:               "x",
		AvailableSubagents: []string{"writer"},
	})
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if prov.calls != 3 {
		t.Errorf("calls = %d, want 3 (maxAttempts)", prov.calls)
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("err should mention exhaustion; got: %v", err)
	}
}

// ----- Step-count cap test -----

// Even if the LLM emits subagent_type (it shouldn't, since we don't
// even include the field in the schema), the planner ignores it and
// leaves PlanStep.SubagentType empty — Scheduler resolves at dispatch.
func TestLLMPlanner_IgnoresSubagentTypeInLLMOutput(t *testing.T) {
	// Provide a JSON shape that includes subagent_type — the parser
	// should silently drop it.
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "x",
		"steps": []map[string]any{
			{"id": "s1", "description": "x", "prompt": "x", "subagent_type": "writer"},
			{"id": "s2", "description": "y", "prompt": "y", "subagent_type": "researcher", "depends_on": []string{"s1"}},
		},
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	out, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "x",
		AvailableSubagents: []string{"writer", "researcher"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for i, s := range out.Plan.Steps {
		if s.SubagentType != "" {
			t.Errorf("step %d: SubagentType = %q, want empty (Scheduler picks at dispatch)",
				i, s.SubagentType)
		}
	}
}

// The emit_plan tool's JSON schema must not even ADVERTISE
// subagent_type as an allowed field. Otherwise an LLM may "helpfully"
// fill it in and the test above only catches it after the fact.
func TestPlannerEmitTool_SchemaHasNoSubagentTypeField(t *testing.T) {
	tool := plannerEmitTool(8)
	props := tool.InputSchema["properties"].(map[string]any)
	steps := props["steps"].(map[string]any)
	items := steps["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	if _, present := itemProps["subagent_type"]; present {
		t.Error("emit_plan schema should NOT include subagent_type — planner is roster-agnostic")
	}
}

func TestLLMPlanner_RejectsTooManySteps(t *testing.T) {
	steps := make([]map[string]any, 0, 5)
	for i := 1; i <= 5; i++ {
		steps = append(steps, map[string]any{
			"id":          "s" + string(rune('0'+i)),
			"description": "step",
			"prompt":      "do",
		})
	}
	planJSON := mustMarshal(t, map[string]any{
		"rationale": "too many",
		"steps":     steps,
	})
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){emitPlanStream(planJSON)},
	}
	planner := newPlannerForTest(prov).WithMaxSteps(3)
	_, err := planner.Plan(context.Background(), PlannerInput{
		Goal:               "x",
		AvailableSubagents: []string{"writer"},
	})
	if err == nil {
		t.Fatal("expected error for plan exceeding maxSteps")
	}
}

// ----- Tool-use absence test -----

func TestLLMPlanner_FailsWhenLLMSkipsTool(t *testing.T) {
	prov := &fakePlannerProvider{
		attempts: []func() ([]types.StreamEvent, error){
			func() ([]types.StreamEvent, error) {
				// Model emitted only text — no emit_plan call.
				return []types.StreamEvent{
					{Type: types.StreamEventText, Text: "I'll just describe the plan in prose."},
					{Type: types.StreamEventMessageEnd, StopReason: "end_turn"},
				}, nil
			},
			func() ([]types.StreamEvent, error) {
				return []types.StreamEvent{
					{Type: types.StreamEventText, Text: "still just text"},
					{Type: types.StreamEventMessageEnd, StopReason: "end_turn"},
				}, nil
			},
			func() ([]types.StreamEvent, error) {
				return []types.StreamEvent{
					{Type: types.StreamEventText, Text: "and again"},
					{Type: types.StreamEventMessageEnd, StopReason: "end_turn"},
				}, nil
			},
		},
	}
	_, err := newPlannerForTest(prov).Plan(context.Background(), PlannerInput{
		Goal:               "x",
		AvailableSubagents: []string{"writer"},
	})
	if err == nil {
		t.Fatal("expected error when model never calls emit_plan")
	}
	if prov.calls != 3 {
		t.Errorf("expected 3 attempts before giving up; got %d", prov.calls)
	}
}

// ----- Schema sanity test -----

func TestPlannerEmitTool_ShapeIsValid(t *testing.T) {
	tool := plannerEmitTool(8)
	if tool.Name != "emit_plan" {
		t.Errorf("tool name = %q", tool.Name)
	}
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties wrong type")
	}
	steps, ok := props["steps"].(map[string]any)
	if !ok {
		t.Fatal("missing steps field")
	}
	if got := steps["maxItems"]; got != 8 {
		t.Errorf("maxItems = %v, want 8", got)
	}
}
