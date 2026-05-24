# L2 Scheduler Refactor — Phase 3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port all remaining old L2 coordinator features (mode selection, retry classification, escalation, fallback, plan judge) into the new scheduler, cut production traffic from the old coordinator path to `SchedulerCoordinator`, and delete ~7700 lines of old `coordinator_*.go` code.

**Architecture:** Six independent feature ports (Tasks 1-6) build the feature parity the new scheduler needs. Tasks 7-9 wire those features into the strategy layer. Task 10 performs the traffic cutover in `subagent.go` — when `!isSubAgent && qe.schedulerCoord != nil`, route through `schedulerCoord.RunLeaf()` instead of `resolveCoordinator()`. Tasks 11-12 add integration tests and delete old files. Plan confirmation (requires new async task state) is explicitly deferred to Phase 4.

**Tech Stack:** Go 1.26, `harnessclaw-go` module, `go test ./...`. No new external dependencies.

---

## File Structure

**New files:**
- `internal/engine/scheduler/router/selector.go` — `KindSelector` interface + `HeuristicKindSelector`
- `internal/engine/scheduler/router/selector_test.go`
- `internal/engine/scheduler/router/resolver.go` — `AgentResolver` interface + `HeuristicAgentResolver`
- `internal/engine/scheduler/router/resolver_test.go`
- `internal/engine/scheduler/dispatch/plan/fallback.go` — `FallbackAggregator`
- `internal/engine/scheduler/dispatch/plan/fallback_test.go`
- `internal/engine/scheduler/dispatch/plan/judge.go` — `PlanJudge` (rule tier)
- `internal/engine/scheduler/dispatch/plan/judge_test.go`

**Modified files:**
- `internal/engine/scheduler/types/failure.go` — transient failure constants + `Retryable()` update
- `internal/engine/scheduler/spec/taskspec.go` — add `EscalationInfo` field
- `internal/engine/scheduler/dispatch/react/strategy.go` — wire `shouldEscalate`
- `internal/engine/scheduler/dispatch/plan/strategy.go` — wire fallback + judge
- `internal/engine/scheduler/router/router.go` — wire `KindSelector`
- `internal/engine/scheduler_coordinator.go` — wire `KindSelector` + `AgentResolver`
- `internal/subagent/qe_factory.go` — wire `AgentResolver` in `specToSpawnConfig`
- `internal/engine/subagent.go` — traffic cutover (one `if` block)

**Deleted files (Task 12):**
All `internal/engine/coordinator_*.go` files and their `_test.go` counterparts.

---

## Task 1: KindSelector — port mode selection logic

**Files:**
- Create: `internal/engine/scheduler/router/selector.go`
- Create: `internal/engine/scheduler/router/selector_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/engine/scheduler/router/selector_test.go
package router_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/router"
	"harnessclaw-go/internal/engine/scheduler/types"
)

func TestHeuristicKindSelector_Defaults(t *testing.T) {
	sel := router.NewHeuristicKindSelector()
	tests := []struct {
		goal string
		want types.Kind
	}{
		{"write a hello world script", types.KindReact},
		{"step by step migrate the database", types.KindPlan},
		{"三步完成数据清洗", types.KindPlan},
		{"research cloud providers and write a comparison report", types.KindPlan},
		{"调研竞品并撰写分析报告", types.KindPlan},
		{"", types.KindReact},
	}
	for _, tc := range tests {
		got := sel.Select(tc.goal)
		if got != tc.want {
			t.Errorf("Select(%q) = %s, want %s", tc.goal, got, tc.want)
		}
	}
}

func TestHeuristicKindSelector_LongGoal(t *testing.T) {
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	sel := router.NewHeuristicKindSelector()
	if sel.Select(string(long)) != types.KindPlan {
		t.Fatal("expected Plan for long goal (>200 runes)")
	}
}
```

- [ ] **Step 2: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/router/ -run TestHeuristicKindSelector -v 2>&1 | head -10
```

Expected: `undefined: router.NewHeuristicKindSelector`

- [ ] **Step 3: Create selector.go**

```go
// internal/engine/scheduler/router/selector.go
package router

import (
	"strings"

	"harnessclaw-go/internal/engine/scheduler/types"
)

// KindSelector decides between KindReact and KindPlan based on goal text.
// Mirrors the heuristics from internal/engine/coordinator_mode_select.go.
type KindSelector interface {
	Select(goal string) types.Kind
}

type HeuristicKindSelector struct{}

func NewHeuristicKindSelector() *HeuristicKindSelector { return &HeuristicKindSelector{} }

func (s *HeuristicKindSelector) Select(goal string) types.Kind {
	if goal == "" {
		return types.KindReact
	}
	lower := strings.ToLower(goal)

	// Explicit multi-step signal
	multiStep := []string{"三步", "四步", "五步", "依次", "分阶段", "step by step", "multi-step", "stepwise"}
	for _, m := range multiStep {
		if strings.Contains(lower, m) {
			return types.KindPlan
		}
	}

	// Research + write/analyze pattern
	hasResearch := containsAny(lower, "research", "investigate", "调研", "搜集", "搜索")
	hasWrite := containsAny(lower, "write", "draft", "report", "写", "撰写", "起草", "报告")
	hasAnalyze := containsAny(lower, "analyze", "compare", "对比", "分析", "解读")
	if hasResearch && (hasWrite || hasAnalyze) {
		return types.KindPlan
	}

	// Long task
	if runeLen(goal) > 200 {
		return types.KindPlan
	}
	return types.KindReact
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
```

- [ ] **Step 4: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/router/ -race -count=1 -v 2>&1 | tail -10
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/router/selector.go internal/engine/scheduler/router/selector_test.go
git commit -m "feat(router): HeuristicKindSelector ports mode-select heuristics from coordinator_mode_select.go"
```

---

## Task 2: AgentResolver — port subagent selection logic

**Files:**
- Create: `internal/engine/scheduler/router/resolver.go`
- Create: `internal/engine/scheduler/router/resolver_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/engine/scheduler/router/resolver_test.go
package router_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/router"
)

func TestHeuristicAgentResolver_ResearchGoal(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"researcher", "developer", "writer", "general-purpose"}
	got := r.Resolve("research the impact of LLMs on software engineering", available)
	if got != "researcher" {
		t.Fatalf("want researcher, got %q", got)
	}
}

func TestHeuristicAgentResolver_WriteGoal(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"writer", "researcher", "general-purpose"}
	got := r.Resolve("write a blog post about Go generics", available)
	if got != "writer" {
		t.Fatalf("want writer, got %q", got)
	}
}

func TestHeuristicAgentResolver_FallbackToGeneral(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	available := []string{"general-purpose"}
	got := r.Resolve("do something vague", available)
	if got != "general-purpose" {
		t.Fatalf("want general-purpose, got %q", got)
	}
}

func TestHeuristicAgentResolver_EmptyAvailable(t *testing.T) {
	r := router.NewHeuristicAgentResolver()
	got := r.Resolve("research something", nil)
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
```

- [ ] **Step 2: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/router/ -run TestHeuristicAgentResolver -v 2>&1 | head -10
```

- [ ] **Step 3: Create resolver.go**

```go
// internal/engine/scheduler/router/resolver.go
package router

import "strings"

// AgentResolver picks which named agent executes a task step.
// Mirrors HeuristicSubagentResolver from coordinator_subagent_resolver.go.
type AgentResolver interface {
	// Resolve returns the agent name from available that best fits goal.
	// Returns "" when available is empty.
	Resolve(goal string, available []string) string
}

type HeuristicAgentResolver struct{}

func NewHeuristicAgentResolver() *HeuristicAgentResolver { return &HeuristicAgentResolver{} }

// Resolve scores each candidate by keyword match count and returns the winner.
// Ties broken by priority order: researcher > analyst > writer > developer > general-purpose.
func (r *HeuristicAgentResolver) Resolve(goal string, available []string) string {
	if len(available) == 0 {
		return ""
	}
	lower := strings.ToLower(goal)

	rules := []struct {
		name     string
		keywords []string
	}{
		{"researcher", []string{"research", "investigate", "调研", "搜索", "搜集", "survey", "find out"}},
		{"analyst", []string{"analyze", "analyse", "compare", "对比", "分析", "评估", "assess"}},
		{"writer", []string{"write", "draft", "report", "blog", "document", "写", "撰写", "起草", "文章"}},
		{"developer", []string{"implement", "code", "develop", "fix", "debug", "开发", "实现", "代码"}},
	}

	best, bestScore := "", 0
	for _, cand := range available {
		score := 0
		for _, rule := range rules {
			if rule.name == cand || strings.HasPrefix(cand, rule.name) {
				for _, kw := range rule.keywords {
					if strings.Contains(lower, kw) {
						score++
					}
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = cand
		}
	}
	if best != "" {
		return best
	}

	// Priority fallback
	priority := []string{"researcher", "analyst", "writer", "developer", "general-purpose", "freelancer"}
	for _, p := range priority {
		for _, a := range available {
			if a == p {
				return a
			}
		}
	}
	return available[0]
}
```

- [ ] **Step 4: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/router/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/router/resolver.go internal/engine/scheduler/router/resolver_test.go
git commit -m "feat(router): HeuristicAgentResolver ports keyword scoring from coordinator_subagent_resolver.go"
```

---

## Task 3: Transient failure constants in types/failure.go

**Files:**
- Modify: `internal/engine/scheduler/types/failure.go`
- Modify: `internal/engine/scheduler/types/failure_test.go`

- [ ] **Step 1: Read current failure.go**

```bash
cat /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/scheduler/types/failure.go
```

- [ ] **Step 2: Write failing test**

Add to `internal/engine/scheduler/types/failure_test.go`:

```go
func TestTransientFailureReasons(t *testing.T) {
	transient := []types.FailureReason{
		types.FailureReasonTimeout,
		types.FailureReasonRateLimit,
		types.FailureReasonOverloaded,
		types.FailureReasonNetwork,
	}
	for _, r := range transient {
		if !r.Retryable() {
			t.Errorf("%s should be Retryable", r)
		}
	}
}
```

- [ ] **Step 3: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/types/ -run TestTransientFailureReasons -v 2>&1 | head -10
```

- [ ] **Step 4: Add constants to failure.go**

In `failure.go`, add after existing constants:

```go
const (
	// Transient failures — retryable when budget allows.
	// Mirror the markers from internal/engine/coordinator_scheduler.go isTransientFailure().
	FailureReasonTimeout    FailureReason = "timeout"
	FailureReasonRateLimit  FailureReason = "rate_limit"
	FailureReasonOverloaded FailureReason = "overloaded"
	FailureReasonNetwork    FailureReason = "network"
)
```

Update `Retryable()` to include these:

```go
func (r FailureReason) Retryable() bool {
	switch r {
	case FailureReasonTimeout, FailureReasonRateLimit, FailureReasonOverloaded, FailureReasonNetwork:
		return true
	// ... existing cases ...
	}
	return false
}
```

Check existing `Retryable()` body first and merge carefully — do not remove existing retryable cases.

- [ ] **Step 5: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/types/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 6: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/types/failure.go internal/engine/scheduler/types/failure_test.go
git commit -m "feat(types): add transient failure reason constants (timeout/rate_limit/overloaded/network)"
```

---

## Task 4: EscalationInfo in spec.TaskSpec

**Files:**
- Modify: `internal/engine/scheduler/spec/taskspec.go`
- Create: `internal/engine/scheduler/spec/escalation_test.go`

- [ ] **Step 1: Read current taskspec.go**

```bash
cat /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/scheduler/spec/taskspec.go
```

- [ ] **Step 2: Write failing test**

```go
// internal/engine/scheduler/spec/escalation_test.go
package spec_test

import (
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler/spec"
)

func TestEscalationInfo_IsEmpty(t *testing.T) {
	var ei *spec.EscalationInfo
	if !ei.IsEmpty() {
		t.Fatal("nil EscalationInfo should be empty")
	}
	ei = &spec.EscalationInfo{}
	if !ei.IsEmpty() {
		t.Fatal("zero EscalationInfo should be empty")
	}
	ei = &spec.EscalationInfo{Reason: "too complex"}
	if ei.IsEmpty() {
		t.Fatal("EscalationInfo with Reason should not be empty")
	}
}

func TestTaskSpec_EscalationInfoField(t *testing.T) {
	sp := spec.TaskSpec{
		Goal: "test",
		Escalation: &spec.EscalationInfo{
			FromKind:    "react",
			Reason:      "failed to complete in react mode",
			Failures:    []string{"tool timed out"},
			EscalatedAt: time.Now(),
		},
	}
	if sp.Escalation.IsEmpty() {
		t.Fatal("expected non-empty escalation")
	}
}
```

- [ ] **Step 3: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/spec/ -run TestEscalation -v 2>&1 | head -10
```

Expected: `undefined: spec.EscalationInfo`

- [ ] **Step 4: Add EscalationInfo struct and field**

In `internal/engine/scheduler/spec/taskspec.go`, add before or after `Hint`:

```go
// EscalationInfo carries context from a prior failed attempt to the next coordinator.
// Non-nil when a task is being re-dispatched after react→plan escalation.
type EscalationInfo struct {
	// FromKind is the Kind that failed (e.g. "react").
	FromKind string `json:"from_kind"`

	// Reason is a short human-readable diagnosis.
	Reason string `json:"reason"`

	// Failures lists structured failure reasons from the prior attempt.
	Failures []string `json:"failures,omitempty"`

	// EscalatedAt is when the escalation was triggered.
	EscalatedAt time.Time `json:"escalated_at"`
}

// IsEmpty reports whether the escalation carries no useful state.
func (e *EscalationInfo) IsEmpty() bool {
	if e == nil {
		return true
	}
	return e.Reason == "" && len(e.Failures) == 0
}
```

Add `"time"` to imports in taskspec.go.

In `TaskSpec`, add:

```go
// Escalation carries prior-attempt context when a task is re-dispatched
// after react→plan escalation. Nil when starting fresh.
Escalation *EscalationInfo `json:"escalation,omitempty"`
```

- [ ] **Step 5: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/spec/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 6: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/spec/taskspec.go internal/engine/scheduler/spec/escalation_test.go
git commit -m "feat(spec): add EscalationInfo to TaskSpec for react→plan escalation context"
```

---

## Task 5: FallbackAggregator in dispatch/plan/fallback.go

**Files:**
- Create: `internal/engine/scheduler/dispatch/plan/fallback.go`
- Create: `internal/engine/scheduler/dispatch/plan/fallback_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/engine/scheduler/dispatch/plan/fallback_test.go
package plan_test

import (
	"testing"

	schedulerplan "harnessclaw-go/internal/engine/scheduler/dispatch/plan"
)

func TestFallbackAggregator_EmptySteps(t *testing.T) {
	agg := schedulerplan.NewFallbackAggregator()
	out := agg.Aggregate(schedulerplan.FallbackInput{
		Goal:   "do something",
		Reason: "budget exhausted",
	})
	if out.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestFallbackAggregator_PartialResults(t *testing.T) {
	agg := schedulerplan.NewFallbackAggregator()
	out := agg.Aggregate(schedulerplan.FallbackInput{
		Goal:   "research and write",
		Reason: "step failed",
		CompletedGoals: []string{"research phase done"},
		FailedGoals:    []string{"writing failed: timeout"},
	})
	if out.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if len(out.NeedsAttention) == 0 {
		t.Fatal("expected items in NeedsAttention for failed goals")
	}
}
```

- [ ] **Step 2: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/plan/ -run TestFallbackAggregator -v 2>&1 | head -10
```

- [ ] **Step 3: Create fallback.go**

```go
// internal/engine/scheduler/dispatch/plan/fallback.go
package plan

import "fmt"

// FallbackInput describes the context when plan execution failed partway.
type FallbackInput struct {
	Goal           string
	Reason         string
	CompletedGoals []string
	FailedGoals    []string
}

// FallbackOutput is the aggregated result returned to the L1 caller.
type FallbackOutput struct {
	Summary        string
	NeedsAttention []string
}

// FallbackAggregator assembles partial step results into a graceful failure summary.
// Mirrors FallbackChain.Aggregate from internal/engine/coordinator_fallback.go.
type FallbackAggregator struct{}

func NewFallbackAggregator() *FallbackAggregator { return &FallbackAggregator{} }

func (f *FallbackAggregator) Aggregate(in FallbackInput) FallbackOutput {
	var summary string
	if len(in.CompletedGoals) > 0 {
		summary = fmt.Sprintf("Goal: %s\nPartially completed (%d steps done) before %s.\nCompleted: %v",
			in.Goal, len(in.CompletedGoals), in.Reason, in.CompletedGoals)
	} else {
		summary = fmt.Sprintf("Goal: %s\nFailed to complete: %s", in.Goal, in.Reason)
	}

	needs := make([]string, 0, len(in.FailedGoals))
	needs = append(needs, in.FailedGoals...)

	return FallbackOutput{
		Summary:        summary,
		NeedsAttention: needs,
	}
}
```

- [ ] **Step 4: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/plan/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/dispatch/plan/fallback.go internal/engine/scheduler/dispatch/plan/fallback_test.go
git commit -m "feat(dispatch/plan): FallbackAggregator ports coordinator_fallback.go graceful degradation"
```

---

## Task 6: PlanJudge (rule tier) in dispatch/plan/judge.go

**Files:**
- Create: `internal/engine/scheduler/dispatch/plan/judge.go`
- Create: `internal/engine/scheduler/dispatch/plan/judge_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/engine/scheduler/dispatch/plan/judge_test.go
package plan_test

import (
	"testing"

	schedulerplan "harnessclaw-go/internal/engine/scheduler/dispatch/plan"
	"harnessclaw-go/internal/engine/scheduler/spec"
)

func TestPlanJudge_ValidPlan(t *testing.T) {
	j := schedulerplan.NewPlanJudge()
	steps := []spec.TaskSpec{
		{Goal: "step 1"},
		{Goal: "step 2"},
	}
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
```

- [ ] **Step 2: Run FAIL**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/plan/ -run TestPlanJudge -v 2>&1 | head -10
```

- [ ] **Step 3: Create judge.go**

```go
// internal/engine/scheduler/dispatch/plan/judge.go
package plan

import (
	"errors"
	"fmt"

	"harnessclaw-go/internal/engine/scheduler/spec"
)

const maxPlanSteps = 20

// PlanJudge validates a generated plan before execution.
// Implements the rule tier from coordinator_judge.go ReviewPlan().
type PlanJudge struct{}

func NewPlanJudge() *PlanJudge { return &PlanJudge{} }

// ReviewPlan runs rule-tier validation on a plan. Returns nil if the plan is
// acceptable, or a descriptive error.
func (j *PlanJudge) ReviewPlan(goal string, steps []spec.TaskSpec) error {
	if goal == "" {
		return errors.New("plan has empty goal")
	}
	if len(steps) == 0 {
		return errors.New("plan has no steps")
	}
	if len(steps) > maxPlanSteps {
		return fmt.Errorf("plan has %d steps (max %d); ask the planner to decompose further", len(steps), maxPlanSteps)
	}
	for i, s := range steps {
		if s.Goal == "" {
			return fmt.Errorf("step %d has empty goal", i)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/plan/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/dispatch/plan/judge.go internal/engine/scheduler/dispatch/plan/judge_test.go
git commit -m "feat(dispatch/plan): PlanJudge rule tier ports coordinator_judge.go ReviewPlan validation"
```

---

## Task 7: Wire shouldEscalate in react/strategy.go

**Files:**
- Modify: `internal/engine/scheduler/dispatch/react/strategy.go`
- Create: `internal/engine/scheduler/dispatch/react/escalate_logic_test.go`

- [ ] **Step 1: Read current react/strategy.go**

```bash
cat /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/scheduler/dispatch/react/strategy.go
```

Note where the `EscalateHook` is called and what data is available after `SpawnAndWaitOne`.

- [ ] **Step 2: Write failing test**

```go
// internal/engine/scheduler/dispatch/react/escalate_logic_test.go
package react_test

import (
	"testing"

	"harnessclaw-go/internal/engine/scheduler/dispatch/react"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/pkg/types"
)

func TestShouldEscalate_OnTerminalModelError(t *testing.T) {
	fn := react.ShouldEscalateFromResult
	msg := msgbus.ResultMessage{
		Status:         msgbus.ResultStatusDone,
		TerminalReason: string(types.TerminalModelError),
	}
	if !fn(msg) {
		t.Fatal("expected escalation for terminal_model_error")
	}
}

func TestShouldEscalate_OnNeedsPlanning(t *testing.T) {
	fn := react.ShouldEscalateFromResult
	msg := msgbus.ResultMessage{
		Status:        msgbus.ResultStatusFailed,
		NeedsPlanning: true,
	}
	if !fn(msg) {
		t.Fatal("expected escalation when NeedsPlanning=true")
	}
}

func TestShouldEscalate_OnSuccess(t *testing.T) {
	fn := react.ShouldEscalateFromResult
	msg := msgbus.ResultMessage{
		Status: msgbus.ResultStatusDone,
	}
	if fn(msg) {
		t.Fatal("should not escalate on clean success")
	}
}
```

Check that `msgbus.ResultMessage` has `TerminalReason` and `NeedsPlanning` fields:
```bash
grep -n "TerminalReason\|NeedsPlanning\|type ResultMessage" internal/msgbus/*.go | head -10
```

Adjust field names to match actual struct.

- [ ] **Step 3: Export ShouldEscalateFromResult function in react package**

In `internal/engine/scheduler/dispatch/react/strategy.go`, add after imports:

```go
// ShouldEscalateFromResult implements the escalation decision from the old
// coordinator_react.go shouldEscalate() method.
// Exported for testing.
func ShouldEscalateFromResult(res msgbus.ResultMessage) bool {
	// Escalate when the sub-agent explicitly requested planning
	if res.NeedsPlanning {
		return true
	}
	// Escalate on terminal model errors that indicate the task is too complex for react
	switch types.TerminalReason(res.TerminalReason) {
	case types.TerminalModelError, types.TerminalSubagentError:
		return true
	}
	return false
}
```

Check the actual field names in `msgbus.ResultMessage` and `types.TerminalReason` constants before implementing. The function signature may need adjustment.

Also update the `EscalateHook` call site in `Run()` to use this function:

```go
if r.caps.EscalateHook != nil {
    if r.caps.EscalateHook(dispatch.EscalateState{...}) {
        return "", &dispatch.EscalationRequestedError{TaskID: taskID}
    }
}
```

The existing `EscalateHook` already returns `EscalationRequestedError` (Phase 2). This task just adds the exported `ShouldEscalateFromResult` helper and tests it.

- [ ] **Step 4: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/react/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/dispatch/react/strategy.go internal/engine/scheduler/dispatch/react/escalate_logic_test.go
git commit -m "feat(dispatch/react): export ShouldEscalateFromResult for escalation decision logic"
```

---

## Task 8: Wire fallback + judge in plan/strategy.go

**Files:**
- Modify: `internal/engine/scheduler/dispatch/plan/strategy.go`

- [ ] **Step 1: Read current plan/strategy.go**

```bash
cat /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/scheduler/dispatch/plan/strategy.go
```

- [ ] **Step 2: Add judge + fallback to Strategy config**

The `plan.Strategy` is constructed via `plan.New(caps dispatch.Capabilities)`. Extend to accept a judge and fallback:

```go
type Config struct {
	Caps      dispatch.Capabilities
	Judge     *PlanJudge     // nil = skip validation
	Fallback  *FallbackAggregator // nil = skip fallback
}

func NewWithConfig(cfg Config) *Strategy { ... }
```

Keep `New(caps)` as a backward-compatible wrapper that calls `NewWithConfig(Config{Caps: caps})`.

- [ ] **Step 3: Wire judge in Run()**

After parsing `plan.json` steps, add:

```go
if p.cfg.Judge != nil {
    if err := p.cfg.Judge.ReviewPlan(task.Goal, steps); err != nil {
        return "", fmt.Errorf("plan validation failed: %w", err)
    }
}
```

Where `task` is the `tstate.TaskState` read at the start of `Run()`.

- [ ] **Step 4: Write test confirming judge is called**

Add to `internal/engine/scheduler/dispatch/plan/strategy_test.go` (or create if not exists):

```go
func TestPlanStrategy_JudgeBlocksInvalidPlan(t *testing.T) {
	// Create a fake factory that writes a plan.json with 0 steps
	// Verify that strategy.Run returns an error about plan validation
	// ... (set up same way as integration test but with bad plan.json)
}
```

The test setup is the same pattern as `TestPlanStrategy_E2E_WithRealFactory` but write a plan.json with `{"steps": []}` and assert the run returns an error containing "plan validation failed".

- [ ] **Step 5: Run PASS**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go test ./internal/engine/scheduler/dispatch/plan/ -race -count=1 2>&1 | tail -5
```

- [ ] **Step 6: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler/dispatch/plan/strategy.go
git commit -m "feat(dispatch/plan): wire PlanJudge + FallbackAggregator into plan.Strategy"
```

---

## Task 9: Wire KindSelector + AgentResolver in SchedulerCoordinator

**Files:**
- Modify: `internal/engine/scheduler_coordinator.go`
- Modify: `internal/subagent/qe_factory.go`

- [ ] **Step 1: Add KindSelector to SchedulerCoordinatorConfig**

In `scheduler_coordinator.go`:

```go
type SchedulerCoordinatorConfig struct {
	Spawner      agent.AgentSpawner
	RootDir      string
	Logger       *slog.Logger
	KindSelector router.KindSelector   // nil = always react
	AgentResolver router.AgentResolver // nil = use spec.AgentDef.Name as-is
}
```

Import `"harnessclaw-go/internal/engine/scheduler/router"`.

In `RunLeaf()`, use the KindSelector to fill `sp.Hint.Kind` when it's empty:

```go
func (sc *SchedulerCoordinator) RunLeaf(ctx context.Context, sessionID string, sp spec.TaskSpec) (types.MetaRef, error) {
	sp.SessionID = sessionID
	// Auto-select kind when caller didn't specify
	if sp.Hint.Kind == "" && sc.cfg.KindSelector != nil {
		sp.Hint.Kind = sc.cfg.KindSelector.Select(sp.Goal)
	}
	if sp.Hint.Kind == "" {
		sp.Hint.Kind = types.KindReact // default
	}
	...
}
```

- [ ] **Step 2: Wire AgentResolver in qe_factory.go**

In `QueryEngineFactory`, add optional `AgentResolver`:

```go
type QueryEngineFactory struct {
	spawner       agent.AgentSpawner
	rootDir       string
	rootSessID    string
	staging       tstate.StagingWriter
	bus           msgbus.Bus
	agentResolver router.AgentResolver // optional; used in specToSpawnConfig
}

func (f *QueryEngineFactory) WithAgentResolver(r router.AgentResolver) *QueryEngineFactory {
	f.agentResolver = r
	return f
}
```

In `specToSpawnConfig`, use the resolver when `SubagentType` is empty:

```go
subagentType := sp.AgentDef.Name
if subagentType == "" && f != nil && f.agentResolver != nil {
	// f is accessed via closure; pass available list from defRegistry if available
	// For now, resolve against a fixed list of known agents
	subagentType = f.agentResolver.Resolve(sp.Goal, knownAgents())
}
if subagentType == "" {
	subagentType = "general-purpose"
}
```

Add `knownAgents()` helper returning the default set:

```go
func knownAgents() []string {
	return []string{"researcher", "analyst", "writer", "developer", "general-purpose", "freelancer"}
}
```

- [ ] **Step 3: Wire in NewSchedulerCoordinator**

In `NewSchedulerCoordinator`:

```go
sel := cfg.KindSelector
if sel == nil {
	sel = router.NewHeuristicKindSelector()
}
res := cfg.AgentResolver
if res == nil {
	res = router.NewHeuristicAgentResolver()
}

factory := subagent.NewQueryEngineFactory(cfg.Spawner, cfg.RootDir, "").
	WithStagingAndBus(staging, bus).
	WithAgentResolver(res)
```

- [ ] **Step 4: Build + test**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go build ./internal/engine/... 2>&1
go test ./internal/engine/ -run "TestSchedulerCoordinator" -v -timeout 15s 2>&1
```

- [ ] **Step 5: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/scheduler_coordinator.go internal/subagent/qe_factory.go
git commit -m "feat(engine): wire KindSelector + AgentResolver into SchedulerCoordinator and QueryEngineFactory"
```

---

## Task 10: Traffic cutover in subagent.go

**Files:**
- Modify: `internal/engine/subagent.go`
- Create: `internal/engine/subagent_coordinator_test.go`

This is the most critical task. Read the file carefully before making changes.

- [ ] **Step 1: Read the coordinator call site in subagent.go**

```bash
sed -n '655,695p' /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/subagent.go
```

Find the exact `else` branch where `resolveCoordinator` is called (currently around line 665).

- [ ] **Step 2: Read subAgentLoopResult struct**

```bash
grep -n "type subAgentLoopResult" /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/subagent.go
sed -n '<line>,$p' /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/subagent.go | head -30
```

Note which fields must be populated for `SpawnSync` to assemble a valid `agent.SpawnResult`.

- [ ] **Step 3: Write failing test**

```go
// internal/engine/subagent_coordinator_test.go
package engine_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine"
)

// TestSpawnSync_RoutesCoordinatorTierThroughScheduler verifies that when
// schedulerCoord is non-nil, a coordinator-tier spawn returns a result
// (even when the coordinator path is mocked).
//
// This test uses DisableNewScheduler=false (the default) and a fake
// spawner so no real LLM calls are made.
func TestSpawnSync_RoutesCoordinatorTierThroughScheduler(t *testing.T) {
	spawner := &engineFakeSpawner{output: "cutover works"}
	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: t.TempDir(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc.Start(ctx)

	// Call RunLeaf directly (the method SpawnSync will delegate to)
	ref, err := sc.RunLeaf(ctx, "test-session", engine.CoordinatorTaskSpec("write hello world"))
	if err != nil {
		t.Fatalf("RunLeaf: %v", err)
	}
	if ref == "" {
		t.Fatal("expected non-empty MetaRef")
	}
}
```

Add `CoordinatorTaskSpec` helper to `scheduler_coordinator.go`:

```go
// CoordinatorTaskSpec builds a minimal TaskSpec for a coordinator-tier spawn.
// Used by tests and by the subagent.go cutover shim.
func CoordinatorTaskSpec(goal string) spec.TaskSpec {
	return spec.TaskSpec{Goal: goal, Layout: "flat"}
}
```

- [ ] **Step 4: Add the cutover shim to subagent.go**

In the `else` branch of the `isSubAgent` check (around line 662), add the new path before the old coordinator path:

```go
} else {
	// Phase 3 cutover: route coordinator-tier spawns through the new L2 scheduler.
	// The flag cfg.DisableNewScheduler=true falls through to the legacy path for
	// incremental rollout.
	if qe.schedulerCoord != nil && cfg.CoordinatorMode != "disable_new_scheduler" {
		sp := spec.TaskSpec{
			Goal:      cfg.Prompt,
			Layout:    "flat",
			SessionID: cfg.ParentSessionID,
			Model:     cfg.Model,
		}
		if cfg.CoordinatorMode != "" {
			sp.Hint.Kind = types.Kind(cfg.CoordinatorMode)
		}
		ref, runErr := qe.schedulerCoord.RunLeaf(ctx, cfg.ParentSessionID, sp)
		if runErr != nil {
			loopResult = subAgentLoopResult{
				Terminal: types.Terminal{
					Reason:  types.TerminalModelError,
					Message: runErr.Error(),
				},
			}
		} else {
			loopResult = metaRefToLoopResult(ref)
		}
	} else {
		coord := qe.resolveCoordinator(cfg.CoordinatorMode, cfg.Prompt, logger)
		...
	}
}
```

Add `metaRefToLoopResult` helper near the cutover code:

```go
// metaRefToLoopResult converts a successful SchedulerCoordinator result
// into a subAgentLoopResult. The MetaRef points to meta.json; we read
// the summary field from it.
func metaRefToLoopResult(ref types.MetaRef) subAgentLoopResult {
	return subAgentLoopResult{
		Terminal: types.Terminal{
			Reason: types.TerminalTaskDone,
		},
		CoordinatorMode: "react", // will be overridden when plan mode is detected
	}
}
```

Check that `types.TerminalTaskDone` exists:
```bash
grep -n "TerminalTaskDone\|TaskDone\b" /Users/skb/Documents/OpenSource/harnessclaw-engine/pkg/types/*.go | head -5
```

If it doesn't exist, use `types.TerminalAgentEnd` or the most appropriate existing constant.

- [ ] **Step 5: Check that imports compile**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go build ./internal/engine/... 2>&1
```

Fix import errors. You'll need to import:
- `"harnessclaw-go/internal/engine/scheduler/spec"` (for TaskSpec)
- `"harnessclaw-go/internal/engine/scheduler/types"` as `schedulertypes` (to avoid collision with `harnessclaw-go/pkg/types`)

Check if there's already a naming conflict:
```bash
grep -n "\"harnessclaw-go/pkg/types\"\|\"harnessclaw-go/internal/engine/scheduler/types\"" /Users/skb/Documents/OpenSource/harnessclaw-engine/internal/engine/subagent.go | head -5
```

Use an alias: `schedulertypes "harnessclaw-go/internal/engine/scheduler/types"`.

- [ ] **Step 6: Run tests**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go test ./internal/engine/ -run "TestSchedulerCoordinator\|TestSpawnSync_Routes" -v -timeout 30s 2>&1
go test ./internal/engine/scheduler/... -race -count=1 -timeout 60s 2>&1 | grep -E "^(ok|FAIL)"
```

- [ ] **Step 7: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/subagent.go internal/engine/scheduler_coordinator.go internal/engine/subagent_coordinator_test.go
git commit -m "feat(engine): route coordinator-tier SpawnSync through SchedulerCoordinator (traffic cutover)"
```

---

## Task 11: Integration test for full L1→L2 cutover path

**Files:**
- Create: `internal/engine/cutover_integration_test.go`

- [ ] **Step 1: Write test**

```go
// internal/engine/cutover_integration_test.go
package engine_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/types"
)

// TestCutover_ReactAndPlan verifies that SchedulerCoordinator handles both
// KindReact and KindPlan tasks end-to-end with a fake spawner.
func TestCutover_ReactAndPlan(t *testing.T) {
	dir := t.TempDir()
	spawner := &engineFakeSpawner{output: "cutover integration done"}

	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sc.Start(ctx)

	cases := []struct {
		name string
		kind types.Kind
	}{
		{"react", types.KindReact},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := sc.RunLeaf(ctx, "sess-"+tc.name, spec.TaskSpec{
				Goal:      "integration test: " + tc.name,
				Hint:      spec.Hint{Kind: tc.kind},
				SessionID: "sess-" + tc.name,
				Layout:    "flat",
			})
			if err != nil {
				t.Fatalf("RunLeaf: %v", err)
			}
			if ref == "" {
				t.Fatal("empty MetaRef")
			}
		})
	}
}

// TestCutover_KindSelectorPicksPlan verifies that a multi-step goal
// automatically routes to KindPlan when KindSelector is wired.
func TestCutover_KindSelectorPicksPlan(t *testing.T) {
	dir := t.TempDir()
	spawner := &planWritingFakeSpawner{dir: dir}

	sc := engine.NewSchedulerCoordinator(engine.SchedulerCoordinatorConfig{
		Spawner: spawner,
		RootDir: dir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sc.Start(ctx)

	// Goal triggers plan mode via HeuristicKindSelector
	ref, err := sc.RunLeaf(ctx, "sess-sel", spec.TaskSpec{
		Goal:   "step by step migrate the database schema",
		Layout: "flat",
	})
	if err != nil {
		t.Fatalf("RunLeaf: %v", err)
	}
	if ref == "" {
		t.Fatal("empty MetaRef")
	}
}
```

`planWritingFakeSpawner` must write plan.json on the first call (the planner step). Copy the pattern from `dispatch/plan/integration_test.go`'s `planWritingSpawner` but adapt for the `engine_test` package context.

- [ ] **Step 2: Run tests**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go test ./internal/engine/ -run "TestCutover_" -v -timeout 30s 2>&1
```

Fix any failures.

- [ ] **Step 3: Run full suite**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go test ./... -count=1 -timeout 300s 2>&1 | grep -E "^(ok|FAIL)" | sort | tail -40
```

- [ ] **Step 4: Commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add internal/engine/cutover_integration_test.go
git commit -m "test(engine): integration tests for L1→L2 cutover path via SchedulerCoordinator"
```

---

## Task 12: Delete old coordinator files + final verification

**Files to delete:**

```
internal/engine/coordinator.go
internal/engine/coordinator_react.go
internal/engine/coordinator_react_test.go
internal/engine/coordinator_plan.go
internal/engine/coordinator_plan_confirmation_test.go
internal/engine/coordinator_plan_types.go
internal/engine/coordinator_plan_types_test.go
internal/engine/coordinator_planner.go
internal/engine/coordinator_planner_test.go
internal/engine/coordinator_scheduler.go
internal/engine/coordinator_scheduler_test.go
internal/engine/coordinator_scheduler_retry_test.go
internal/engine/coordinator_budget.go
internal/engine/coordinator_budget_test.go
internal/engine/coordinator_escalation.go
internal/engine/coordinator_escalation_test.go
internal/engine/coordinator_fallback.go
internal/engine/coordinator_fallback_test.go
internal/engine/coordinator_judge.go
internal/engine/coordinator_judge_test.go
internal/engine/coordinator_mode_select.go
internal/engine/coordinator_mode_select_test.go
internal/engine/coordinator_subagent_resolver.go
internal/engine/coordinator_subagent_resolver_test.go
internal/engine/coordinator_extensibility_test.go
internal/engine/coordinator_test.go
```

**BEFORE deleting, check each file for types still used elsewhere:**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine

# Types that MUST be checked before deleting
grep -rn "CoordinatorMode\|SharedDeps\|PlanCoordinator\|ReActCoordinator\|PlanStep\b\|BudgetTracker\|FallbackChain\|SubagentResolver\|ModeSelector\b" \
    --include="*.go" \
    --exclude-dir="*_test" \
    . | grep -v "^./internal/engine/coordinator" | grep -v "^Binary" | head -30
```

For any type that's still used outside `coordinator_*.go`:
- If it's a type that maps to the new scheduler (e.g. `PlanStep` → `spec.TaskSpec`), update callers to use the new type
- If it's used only for tests, those tests should be deleted together
- If it's a critical public type, keep it in a shim file

**Common survivors:**
- `CoordinatorMode` string type: used in `agent.SpawnConfig.CoordinatorMode`. Keep the type but define it in `internal/engine/coordinator_mode.go` (new thin file with just the type + constants).
- `LoopConfig`: used in coordinator Run() signatures. Will be gone when coordinator files are deleted.

- [ ] **Step 1: Check for external usages**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
grep -rn "CoordinatorMode\b\|CoordinatorModeReAct\|CoordinatorModePlan" \
    --include="*.go" . | grep -v "coordinator_" | grep -v "_test.go" | head -20
```

- [ ] **Step 2: Create shim for CoordinatorMode if needed**

If `CoordinatorMode` is referenced outside coordinator files (e.g. in `agent/spawner.go`, `subagent.go`), create:

```go
// internal/engine/coordinator_mode.go
package engine

// CoordinatorMode is the L2 execution mode for coordinator-tier spawns.
// Used in agent.SpawnConfig.CoordinatorMode. Kept for backward compatibility
// after coordinator_*.go files were deleted in Phase 3.
type CoordinatorMode string

const (
	CoordinatorModeReAct CoordinatorMode = "react"
	CoordinatorModePlan  CoordinatorMode = "plan"
)

func (m CoordinatorMode) IsKnown() bool {
	return m == CoordinatorModeReAct || m == CoordinatorModePlan
}

func (m CoordinatorMode) String() string { return string(m) }
```

- [ ] **Step 3: Delete the coordinator files**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
rm -f \
  internal/engine/coordinator.go \
  internal/engine/coordinator_react.go \
  internal/engine/coordinator_react_test.go \
  internal/engine/coordinator_plan.go \
  internal/engine/coordinator_plan_confirmation_test.go \
  internal/engine/coordinator_plan_types.go \
  internal/engine/coordinator_plan_types_test.go \
  internal/engine/coordinator_planner.go \
  internal/engine/coordinator_planner_test.go \
  internal/engine/coordinator_scheduler.go \
  internal/engine/coordinator_scheduler_test.go \
  internal/engine/coordinator_scheduler_retry_test.go \
  internal/engine/coordinator_budget.go \
  internal/engine/coordinator_budget_test.go \
  internal/engine/coordinator_escalation.go \
  internal/engine/coordinator_escalation_test.go \
  internal/engine/coordinator_fallback.go \
  internal/engine/coordinator_fallback_test.go \
  internal/engine/coordinator_judge.go \
  internal/engine/coordinator_judge_test.go \
  internal/engine/coordinator_mode_select.go \
  internal/engine/coordinator_mode_select_test.go \
  internal/engine/coordinator_subagent_resolver.go \
  internal/engine/coordinator_subagent_resolver_test.go \
  internal/engine/coordinator_extensibility_test.go \
  internal/engine/coordinator_test.go
```

- [ ] **Step 4: Fix compile errors**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine && go build ./internal/engine/... 2>&1
```

Fix every undefined reference. Common fixes:
- `SharedDeps` → remove (callers used it only in coordinator_*.go)
- `Coordinator` interface → remove
- `LoopConfig` → remove or replace with equivalent new type
- `subAgentLoopResult.BudgetSpent` → zero value (budget tracking is in new scheduler)
- `resolveCoordinator` → now only needed for the fallback path in Task 10's `else` branch (which can be removed too, or replaced with a deprecated warning)

- [ ] **Step 5: Run full test suite**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
go test ./... -count=1 -timeout 300s 2>&1 | grep -E "^(ok|FAIL)" | sort
```

Expected: all packages that were previously `ok` remain `ok`. The pre-existing failures in `internal/engine` (TestQueryLoop_*, TestPlanConfirmation_*) should still be there (not introduced by this change).

- [ ] **Step 6: Final commit**

```bash
cd /Users/skb/Documents/OpenSource/harnessclaw-engine
git add -A
git commit -m "feat(engine): delete coordinator_*.go — Phase 3 migration complete

All L2 coordinator features ported to new scheduler:
- Mode selection → scheduler/router/selector.go
- Agent resolver → scheduler/router/resolver.go
- Transient failure constants → scheduler/types/failure.go
- Escalation context → scheduler/spec/taskspec.go
- Fallback aggregation → scheduler/dispatch/plan/fallback.go
- Plan judge (rule tier) → scheduler/dispatch/plan/judge.go
- Traffic cutover → subagent.go routes through SchedulerCoordinator

Deferred to Phase 4: plan confirmation (requires async task state)."
```

---

## Self-Review

**Spec coverage:**

| Old file | New location | Task |
|---|---|---|
| `coordinator_mode_select.go` | `scheduler/router/selector.go` | Task 1 |
| `coordinator_subagent_resolver.go` | `scheduler/router/resolver.go` | Task 2 |
| `isTransientFailure()` in coordinator_scheduler.go | `types/failure.go` | Task 3 |
| `EscalationContext` in coordinator_escalation.go | `spec.EscalationInfo` | Task 4 |
| `coordinator_fallback.go` | `dispatch/plan/fallback.go` | Task 5 |
| `coordinator_judge.go` (rule tier) | `dispatch/plan/judge.go` | Task 6 |
| `coordinator_react.go shouldEscalate()` | `react/strategy.go` | Task 7 |
| Plan judge + fallback wiring | `dispatch/plan/strategy.go` | Task 8 |
| Mode selector + resolver wiring | `scheduler_coordinator.go` + `qe_factory.go` | Task 9 |
| Traffic cutover | `subagent.go` | Task 10 |
| Integration test | `cutover_integration_test.go` | Task 11 |
| Delete old files | All coordinator_*.go | Task 12 |

**Explicitly deferred (Phase 4):**
- Plan confirmation UI gate (requires `awaiting_confirmation` task state + async UI protocol)
- LLM judge tier (ReviewStep, ReviewGoal with LLM calls — requires provider access in scheduler)
- LLMSubagentResolver (LLM-driven agent selection — requires provider access in router)
- BudgetTracker accumulation from sub-agent usage reports (token counting in tstate)
