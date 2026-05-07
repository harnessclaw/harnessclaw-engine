package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/pkg/types"
)

// fakeDispatcher records every Dispatch call and returns a programmable
// SpawnResult per skill. Lets us exercise Scheduler in isolation.
type fakeDispatcher struct {
	mu       sync.Mutex
	calls    []*agent.SpawnConfig
	results  map[string]*agent.SpawnResult // skill -> result
	err      map[string]error
	fallback *agent.SpawnResult
}

func (f *fakeDispatcher) Dispatch(_ context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cfg)
	if e, ok := f.err[cfg.SubagentType]; ok {
		return nil, e
	}
	if r, ok := f.results[cfg.SubagentType]; ok {
		return r, nil
	}
	if f.fallback != nil {
		return f.fallback, nil
	}
	return &agent.SpawnResult{Status: "success"}, nil
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{
		results: make(map[string]*agent.SpawnResult),
		err:     make(map[string]error),
	}
}

func newTestSchedulerDeps() *SharedDeps {
	return &SharedDeps{
		Budget: NewBudgetTracker(BudgetLimit{}).Start(),
		Judge:  NewJudge(nil),
	}
}

func TestScheduler_HappyPathSequential(t *testing.T) {
	disp := newFakeDispatcher()
	disp.results["researcher"] = &agent.SpawnResult{
		Status:             "success",
		Output:             "<summary>found 10 sources</summary>",
		SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_research", Role: "research_report"}},
	}
	disp.results["writer"] = &agent.SpawnResult{
		Status:             "success",
		Output:             "<summary>drafted</summary>",
		SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_draft", Role: "draft"}},
	}

	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "research and write",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher", Prompt: "research X"},
			{ID: "s2", SubagentType: "writer", Prompt: "write Y", DependsOn: []string{"s1"}},
		},
	}
	out := sched.Run(context.Background(), plan, &agent.SpawnConfig{ParentSessionID: "sess"})

	if out.Status != "success" {
		t.Fatalf("expected success; got %q (%s)", out.Status, out.Reason)
	}
	if len(out.StepResults) != 2 {
		t.Fatalf("expected 2 step results; got %d", len(out.StepResults))
	}
	if len(disp.calls) != 2 {
		t.Errorf("expected 2 dispatches; got %d", len(disp.calls))
	}
	// The writer step's prompt should reference the upstream artifact.
	if !strings.Contains(disp.calls[1].Prompt, "art_research") {
		t.Errorf("downstream step prompt missing upstream artifact reference; got: %s",
			disp.calls[1].Prompt)
	}
}

func TestScheduler_SkipsDownstreamOnFailure(t *testing.T) {
	disp := newFakeDispatcher()
	disp.err["researcher"] = errors.New("dispatch boom")

	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "fail then write",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher"},
			{ID: "s2", SubagentType: "writer", DependsOn: []string{"s1"}},
		},
	}
	out := sched.Run(context.Background(), plan, nil)

	if out.Status != "failed" {
		t.Errorf("all steps failing/skipped should yield failed; got %q", out.Status)
	}
	if len(disp.calls) != 1 {
		t.Errorf("downstream step should not have been dispatched; got %d calls", len(disp.calls))
	}
	if out.StepResults[1].Status != "skipped" {
		t.Errorf("downstream should be skipped; got %q", out.StepResults[1].Status)
	}
}

func TestScheduler_HaltsOnBudgetExceeded(t *testing.T) {
	disp := newFakeDispatcher()
	disp.fallback = &agent.SpawnResult{
		Status: "success",
		Usage:  &types.Usage{InputTokens: 60, OutputTokens: 60}, // 120 per step
	}

	deps := newTestSchedulerDeps()
	deps.Budget = NewBudgetTracker(BudgetLimit{MaxTokens: 100}).Start()

	sched := NewScheduler(deps, disp, nil)
	plan := &Plan{
		Goal: "burn budget",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer"},
			{ID: "s2", SubagentType: "writer"},
			{ID: "s3", SubagentType: "writer"},
		},
	}
	out := sched.Run(context.Background(), plan, nil)

	if out.Status == "success" {
		t.Errorf("status should reflect partial / budget; got success")
	}
	// One step at most should run (it adds 120 tokens, which already
	// exceeds the 100 limit, triggering the gate before s2 dispatches).
	if len(disp.calls) > 1 {
		t.Errorf("budget gate should stop after the first call; got %d", len(disp.calls))
	}
	if out.Reason == "" {
		t.Errorf("budget-stopped run should explain why")
	}
}

func TestScheduler_ContractFailureMarksFailedAndIncrementsBudget(t *testing.T) {
	disp := newFakeDispatcher()
	disp.results["writer"] = &agent.SpawnResult{
		Status:           "success",
		Output:           "ok",
		ContractFailures: []string{"missing required role"},
	}

	deps := newTestSchedulerDeps()
	sched := NewScheduler(deps, disp, nil)
	plan := &Plan{
		Goal: "must produce draft",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer", ExpectedOutputs: []types.ExpectedOutput{
				{Role: "draft_email", Required: true},
			}},
		},
	}
	out := sched.Run(context.Background(), plan, nil)

	if out.StepResults[0].Status != "failed" {
		t.Errorf("step missing required role should be failed; got %q", out.StepResults[0].Status)
	}
	if deps.Budget.Snapshot().Failures == 0 {
		t.Errorf("Judge failure should increment budget tracker failures")
	}
}

func TestScheduler_EmptyPlan(t *testing.T) {
	sched := NewScheduler(newTestSchedulerDeps(), newFakeDispatcher(), nil)
	out := sched.Run(context.Background(), &Plan{Goal: "x"}, nil)
	if out.Status != "failed" {
		t.Errorf("empty plan should fail; got %q", out.Status)
	}
}

func TestScheduler_PartialMixedOutcome(t *testing.T) {
	disp := newFakeDispatcher()
	disp.results["researcher"] = &agent.SpawnResult{
		Status:             "success",
		Output:             "<summary>ok</summary>",
		SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_a"}},
	}
	disp.err["analyst"] = errors.New("analyst exploded")

	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "research+analyse with independent steps",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher"},
			{ID: "s2", SubagentType: "analyst"},
		},
	}
	out := sched.Run(context.Background(), plan, nil)
	if out.Status != "partial" {
		t.Errorf("mixed success/fail should be partial; got %q", out.Status)
	}
}
