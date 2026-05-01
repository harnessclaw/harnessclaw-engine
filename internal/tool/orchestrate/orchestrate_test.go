package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
)

// scriptedSpawner is the test double for Orchestrate tool tests. It returns
// pre-programmed responses keyed by SpawnConfig.SubagentType + call count.
type scriptedSpawner struct {
	mu              sync.Mutex
	plannerCalls    int
	plannerScript   []spawnReply // one entry per planner attempt
	stepReplies     map[string]*agent.SpawnResult
	stepErrs        map[string]error
	allCalls        []agent.SpawnConfig
}

type spawnReply struct {
	res *agent.SpawnResult
	err error
}

func newScriptedSpawner() *scriptedSpawner {
	return &scriptedSpawner{
		stepReplies: make(map[string]*agent.SpawnResult),
		stepErrs:    make(map[string]error),
	}
}

func (s *scriptedSpawner) addPlannerReply(out string, err error) {
	s.plannerScript = append(s.plannerScript, spawnReply{
		res: &agent.SpawnResult{
			AgentID: "planner",
			Output:  out,
			Summary: "planner",
			Status:  "completed",
		},
		err: err,
	})
}

func (s *scriptedSpawner) setStep(name string, res *agent.SpawnResult, err error) {
	s.stepReplies[name] = res
	s.stepErrs[name] = err
}

func (s *scriptedSpawner) SpawnSync(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allCalls = append(s.allCalls, *cfg)

	if cfg.SubagentType == PlannerSubagentType {
		idx := s.plannerCalls
		s.plannerCalls++
		if idx >= len(s.plannerScript) {
			return nil, errors.New("no planner script entry left")
		}
		r := s.plannerScript[idx]
		return r.res, r.err
	}

	if r, ok := s.stepReplies[cfg.Name]; ok {
		return r, s.stepErrs[cfg.Name]
	}
	// Fallback success.
	return &agent.SpawnResult{
		AgentID: "agent-" + cfg.Name,
		Output:  "<summary>fallback</summary>",
		Summary: "fallback",
		Status:  "completed",
	}, nil
}

const validPlanJSON = "<summary>2 steps</summary>\n\n```json\n{\"steps\":[" +
	"{\"step_id\":\"s1\",\"subagent_type\":\"researcher\",\"task\":\"调研\",\"depends_on\":[]}," +
	"{\"step_id\":\"s2\",\"subagent_type\":\"writer\",\"task\":\"写报告\",\"depends_on\":[\"s1\"]}" +
	"]}\n```"

func TestOrchestrate_Validate(t *testing.T) {
	tool := New(newScriptedSpawner(), NewStaticRoster([]string{"researcher"}), zap.NewNop())

	if err := tool.ValidateInput(json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing intent")
	}
	if err := tool.ValidateInput(json.RawMessage(`{"intent":"  "}`)); err == nil {
		t.Error("expected error for blank intent")
	}
	if err := tool.ValidateInput(json.RawMessage(`{"intent":"do thing"}`)); err != nil {
		t.Errorf("expected valid input, got %v", err)
	}
	if err := tool.ValidateInput(json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestOrchestrate_HappyPath(t *testing.T) {
	sp := newScriptedSpawner()
	sp.addPlannerReply(validPlanJSON, nil)

	tool := New(sp, NewStaticRoster([]string{"researcher", "writer"}), zap.NewNop())

	res, err := tool.Execute(context.Background(),
		json.RawMessage(`{"intent":"准备一份竞品分析报告"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error result: %s", res.Content)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["status"] != "completed" {
		t.Errorf("status = %v, want completed", payload["status"])
	}
	steps, ok := payload["steps"].([]any)
	if !ok || len(steps) != 2 {
		t.Errorf("expected 2 steps in payload, got %v", payload["steps"])
	}
}

func TestOrchestrate_PlannerRetryThenSucceeds(t *testing.T) {
	sp := newScriptedSpawner()
	// 1st: bad JSON
	sp.addPlannerReply("planner output without any JSON at all", nil)
	// 2nd: valid plan
	sp.addPlannerReply(validPlanJSON, nil)

	tool := New(sp, NewStaticRoster([]string{"researcher", "writer"}), zap.NewNop())
	res, err := tool.Execute(context.Background(),
		json.RawMessage(`{"intent":"竞品分析"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error result: %s", res.Content)
	}
	if sp.plannerCalls != 2 {
		t.Errorf("planner called %d times, want 2", sp.plannerCalls)
	}
}

func TestOrchestrate_DegradesAfterMaxAttempts(t *testing.T) {
	sp := newScriptedSpawner()
	// All MaxPlannerAttempts attempts return un-parseable output.
	for i := 0; i < MaxPlannerAttempts; i++ {
		sp.addPlannerReply("just some prose, no json here", nil)
	}

	tool := New(sp, NewStaticRoster([]string{"researcher", "writer"}), zap.NewNop())
	res, err := tool.Execute(context.Background(),
		json.RawMessage(`{"intent":"做个市场调研方案"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Degradation is conveyed via payload, not IsError.
	if res.IsError {
		t.Errorf("degraded path should not set IsError; content=%s", res.Content)
	}
	if sp.plannerCalls != MaxPlannerAttempts {
		t.Errorf("planner called %d times, want %d", sp.plannerCalls, MaxPlannerAttempts)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["status"] != "plan_failed" {
		t.Errorf("status = %v, want plan_failed", payload["status"])
	}
	if payload["degraded"] != true {
		t.Errorf("degraded = %v, want true", payload["degraded"])
	}

	// Metadata must also flag degradation for downstream observability.
	if res.Metadata["degraded"] != true {
		t.Errorf("metadata.degraded = %v, want true", res.Metadata["degraded"])
	}
}

func TestOrchestrate_DegradesOnInvalidPlan(t *testing.T) {
	sp := newScriptedSpawner()
	// Returns a plan that references an unknown subagent_type 3 times.
	bad := "```json\n{\"steps\":[{\"step_id\":\"s1\",\"subagent_type\":\"ghost\",\"task\":\"x\",\"depends_on\":[]}]}\n```"
	for i := 0; i < MaxPlannerAttempts; i++ {
		sp.addPlannerReply(bad, nil)
	}

	tool := New(sp, NewStaticRoster([]string{"researcher", "writer"}), zap.NewNop())
	res, _ := tool.Execute(context.Background(),
		json.RawMessage(`{"intent":"x"}`))

	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["status"] != "plan_failed" || payload["degraded"] != true {
		t.Errorf("validation failure should degrade; payload=%v", payload)
	}
	reason, _ := payload["reason"].(string)
	if !strings.Contains(reason, "ghost") {
		t.Errorf("reason should mention unknown subagent; got %q", reason)
	}
}

func TestOrchestrate_AvailableAgentsOverride(t *testing.T) {
	sp := newScriptedSpawner()
	sp.addPlannerReply(validPlanJSON, nil)

	// Roster says only "writer" is available, but caller explicitly adds
	// "researcher" via available_agents override — the plan should pass.
	tool := New(sp, NewStaticRoster([]string{"writer"}), zap.NewNop())
	input := `{"intent":"竞品分析","available_agents":["researcher"]}`

	res, err := tool.Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(res.Content), &payload)
	if payload["status"] != "completed" {
		t.Errorf("status = %v; override should expand roster. payload=%v", payload["status"], payload)
	}
}

func TestOrchestrate_StepFailureProducesPartialCompleted(t *testing.T) {
	sp := newScriptedSpawner()
	sp.addPlannerReply(validPlanJSON, nil)
	sp.setStep("s1", &agent.SpawnResult{Status: "completed", Summary: "ok",
		Output: "<summary>ok</summary>", AgentID: "a-s1"}, nil)
	sp.setStep("s2", nil, errors.New("write failed"))

	tool := New(sp, NewStaticRoster([]string{"researcher", "writer"}), zap.NewNop())
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"intent":"x"}`))

	var payload map[string]any
	_ = json.Unmarshal([]byte(res.Content), &payload)
	if payload["status"] != "partial_completed" {
		t.Errorf("status = %v, want partial_completed", payload["status"])
	}
}

func TestOrchestrate_AllStepsFailedSetsErrorResult(t *testing.T) {
	sp := newScriptedSpawner()
	// Only one step, planner gives a single-step plan, step always fails.
	singleStep := "```json\n{\"steps\":[{\"step_id\":\"s1\",\"subagent_type\":\"writer\",\"task\":\"x\",\"depends_on\":[]}]}\n```"
	sp.addPlannerReply(singleStep, nil)
	sp.setStep("s1", nil, errors.New("nope"))

	tool := New(sp, NewStaticRoster([]string{"writer"}), zap.NewNop())
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"intent":"x"}`))

	if !res.IsError {
		t.Errorf("expected IsError=true when all steps fail")
	}
}

func TestOrchestrate_RosterSkipsPlannerAndDedupes(t *testing.T) {
	tool := New(newScriptedSpawner(),
		NewStaticRoster([]string{"writer", "writer", "planner", "researcher"}),
		zap.NewNop())
	got := tool.resolveAgents([]string{"writer", "analyst"})

	// Sorted: analyst, researcher, writer  — planner removed, duplicates removed.
	want := []string{"analyst", "researcher", "writer"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOrchestrate_ToolMetadata(t *testing.T) {
	tool := New(newScriptedSpawner(), nil, zap.NewNop())
	if tool.Name() != "Orchestrate" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if !tool.IsLongRunning() {
		t.Error("IsLongRunning() should be true")
	}
	if tool.IsReadOnly() {
		t.Error("IsReadOnly() should be false")
	}
	schema := tool.InputSchema()
	if schema["type"] != "object" {
		t.Errorf("schema.type = %v", schema["type"])
	}
}
