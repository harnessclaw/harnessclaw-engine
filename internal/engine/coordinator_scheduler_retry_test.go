package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/pkg/types"
)

// sequenceDispatcher returns a different agent.SpawnResult / error per
// invocation. Used to model "attempt 1 fails, attempt 2 succeeds" or
// "every attempt fails" patterns for the step-level retry tests.
type sequenceDispatcher struct {
	mu      sync.Mutex
	calls   int
	results []*agent.SpawnResult
	errs    []error
}

func (s *sequenceDispatcher) Dispatch(_ context.Context, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.calls
	s.calls++
	if idx < len(s.errs) && s.errs[idx] != nil {
		return nil, s.errs[idx]
	}
	if idx < len(s.results) && s.results[idx] != nil {
		return s.results[idx], nil
	}
	return &agent.SpawnResult{Status: "success"}, nil
}

func TestScheduler_RetriesOnTransientFailure(t *testing.T) {
	disp := &sequenceDispatcher{
		errs: []error{
			errors.New("provider rate limit hit, please retry later"),
			nil,
		},
		results: []*agent.SpawnResult{
			nil, // first attempt errors
			{
				Status:             "success",
				Output:             "<summary>second attempt ok</summary>",
				SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_ok"}},
			},
		},
	}

	out := make(chan types.EngineEvent, 32)
	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "retry me",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher", Prompt: "do work"},
		},
	}
	res := sched.Run(context.Background(), plan, &agent.SpawnConfig{ParentOut: out})

	if res.Status != "success" {
		t.Fatalf("expected success after retry; got %q (%s)", res.Status, res.Reason)
	}
	if disp.calls != 2 {
		t.Fatalf("expected 2 dispatch attempts; got %d", disp.calls)
	}
	if got := res.StepResults[0].Attempts; got != 2 {
		t.Errorf("expected Attempts=2 after one retry; got %d", got)
	}
}

func TestScheduler_DoesNotRetryOnNonTransientFailure(t *testing.T) {
	disp := &sequenceDispatcher{
		errs: []error{
			// Non-transient: invalid arguments should not retry.
			errors.New("contract violation: missing required role"),
		},
	}

	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "non-transient",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer"},
		},
	}
	res := sched.Run(context.Background(), plan, nil)

	if res.Status != "failed" {
		t.Fatalf("expected failed; got %q", res.Status)
	}
	if disp.calls != 1 {
		t.Errorf("non-transient failure should NOT retry; got %d calls", disp.calls)
	}
	if got := res.StepResults[0].Attempts; got != 1 {
		t.Errorf("expected Attempts=1 (no retry); got %d", got)
	}
}

func TestScheduler_ExhaustsMaxAttemptsOnPersistentTransient(t *testing.T) {
	disp := &sequenceDispatcher{
		errs: []error{
			errors.New("upstream timeout"),
			errors.New("upstream timeout"),
			errors.New("upstream timeout"),
		},
	}

	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "always fails",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher"},
		},
	}
	res := sched.Run(context.Background(), plan, nil)

	if res.Status != "failed" {
		t.Fatalf("expected failed; got %q", res.Status)
	}
	if disp.calls != stepMaxAttempts {
		t.Errorf("expected exactly %d attempts; got %d", stepMaxAttempts, disp.calls)
	}
	if got := res.StepResults[0].Attempts; got != stepMaxAttempts {
		t.Errorf("expected Attempts=%d on exhaustion; got %d", stepMaxAttempts, got)
	}
}

func TestScheduler_EmitsStepStartedPerAttempt(t *testing.T) {
	disp := &sequenceDispatcher{
		errs: []error{
			errors.New("503 service overloaded"),
			nil,
		},
		results: []*agent.SpawnResult{
			nil,
			{Status: "success", Output: "<summary>ok</summary>"},
		},
	}

	out := make(chan types.EngineEvent, 64)
	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "watch attempts",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer"},
		},
	}
	_ = sched.Run(context.Background(), plan, &agent.SpawnConfig{ParentOut: out})
	close(out)

	var startedAttempts []int
	var sawStepCompleted bool
	var completedAttempts int
	for ev := range out {
		switch ev.Type {
		case types.EngineEventStepStarted:
			if ev.TaskDispatch == nil {
				t.Fatalf("step_started missing TaskDispatch payload")
			}
			startedAttempts = append(startedAttempts, ev.TaskDispatch.Attempts)
		case types.EngineEventStepCompleted:
			sawStepCompleted = true
			if ev.TaskDispatch != nil {
				completedAttempts = ev.TaskDispatch.Attempts
			}
		}
	}

	if len(startedAttempts) != 2 {
		t.Fatalf("expected 2 step_started events (one per attempt); got %d (%v)",
			len(startedAttempts), startedAttempts)
	}
	if startedAttempts[0] != 1 || startedAttempts[1] != 2 {
		t.Errorf("expected attempts [1,2] on step_started; got %v", startedAttempts)
	}
	if !sawStepCompleted {
		t.Errorf("expected step_completed at end of retried run")
	}
	if completedAttempts != 2 {
		t.Errorf("step_completed should carry cumulative Attempts=2; got %d", completedAttempts)
	}
}

func TestScheduler_EmitStepFailedCarriesErrorTypeAndRetryable(t *testing.T) {
	disp := &sequenceDispatcher{
		errs: []error{
			errors.New("rate limit exceeded"),
			errors.New("rate limit exceeded"),
		},
	}

	out := make(chan types.EngineEvent, 32)
	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "always rate limited",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher"},
		},
	}
	_ = sched.Run(context.Background(), plan, &agent.SpawnConfig{ParentOut: out})
	close(out)

	var failed *types.TaskDispatch
	for ev := range out {
		if ev.Type == types.EngineEventStepFailed {
			failed = ev.TaskDispatch
		}
	}
	if failed == nil {
		t.Fatalf("expected step_failed event")
	}
	if failed.ErrorType != string(emit.ErrorTypeToolRateLimited) {
		t.Errorf("expected error_type=%s; got %q",
			emit.ErrorTypeToolRateLimited, failed.ErrorType)
	}
	if !failed.Retryable {
		t.Errorf("rate-limit failure should be marked retryable on the wire")
	}
	if failed.Attempts != stepMaxAttempts {
		t.Errorf("step_failed should report cumulative Attempts=%d; got %d",
			stepMaxAttempts, failed.Attempts)
	}
}

func TestIsTransientFailure(t *testing.T) {
	cases := []struct {
		name string
		res  *StepResult
		want bool
	}{
		{"nil", nil, false},
		{"success status", &StepResult{Status: "success", Failures: []string{"timeout"}}, false},
		{"empty failures", &StepResult{Status: "failed"}, false},
		{"timeout", &StepResult{Status: "failed", Failures: []string{"request timeout"}}, true},
		{"deadline exceeded", &StepResult{Status: "failed", Failures: []string{"context deadline exceeded"}}, true},
		{"rate limit", &StepResult{Status: "failed", Failures: []string{"429 rate limit"}}, true},
		{"overloaded", &StepResult{Status: "failed", Failures: []string{"server overloaded"}}, true},
		{"503", &StepResult{Status: "failed", Failures: []string{"got 503 from provider"}}, true},
		{"connection reset", &StepResult{Status: "failed", Failures: []string{"connection reset by peer"}}, true},
		{"contract", &StepResult{Status: "failed", Failures: []string{"contract violation: missing role"}}, false},
		{"upstream dep", &StepResult{Status: "failed", Failures: []string{"upstream dep s0 did not succeed"}}, false},
		{"resolver", &StepResult{Status: "failed", Failures: []string{"subagent resolution: no L3 registered"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientFailure(tc.res); got != tc.want {
				var label []string
				if tc.res != nil {
					label = tc.res.Failures
				}
				t.Errorf("isTransientFailure(%v) = %v; want %v", label, got, tc.want)
			}
		})
	}
}

func TestClassifyStepErrorType(t *testing.T) {
	cases := []struct {
		name     string
		failures []string
		want     emit.ErrorType
	}{
		{"empty", nil, emit.ErrorTypeInternal},
		{"rate limit", []string{"rate limit exceeded"}, emit.ErrorTypeToolRateLimited},
		{"overloaded 503", []string{"503 overloaded"}, emit.ErrorTypeOverloaded},
		{"timeout 504", []string{"504 gateway timeout"}, emit.ErrorTypeToolTimeout},
		{"deadline", []string{"context deadline exceeded"}, emit.ErrorTypeToolTimeout},
		{"resolver miss", []string{"subagent resolution: no L3 registered"}, emit.ErrorTypeDependencyFail},
		{"upstream skip", []string{"upstream dep s0 did not succeed"}, emit.ErrorTypeDependencyFail},
		{"contract", []string{"contract violation"}, emit.ErrorTypeInternal},
		{"invalid schema", []string{"invalid schema for SubmitTaskResult"}, emit.ErrorTypeInternal},
		{"unknown", []string{"weird thing"}, emit.ErrorTypeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStepErrorType(tc.failures); got != tc.want {
				t.Errorf("classifyStepErrorType(%v) = %q; want %q",
					tc.failures, got, tc.want)
			}
		})
	}
}

func TestScheduler_FirstAttemptSuccessRecordsAttemptsOne(t *testing.T) {
	// Sanity check: success on first try should still populate Attempts=1
	// so emit step_completed includes the field consistently regardless
	// of whether a retry occurred.
	disp := &sequenceDispatcher{
		results: []*agent.SpawnResult{
			{Status: "success", Output: "<summary>fast</summary>"},
		},
	}
	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "happy",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "writer"},
		},
	}
	res := sched.Run(context.Background(), plan, nil)
	if disp.calls != 1 {
		t.Errorf("happy path should dispatch once; got %d", disp.calls)
	}
	if got := res.StepResults[0].Attempts; got != 1 {
		t.Errorf("expected Attempts=1 on first-try success; got %d", got)
	}
}

// TestScheduler_TerminalModelErrorRetriesAndSurfacesReason pins the
// "silent step failure" fix: when a sub-agent returns a SpawnResult with
// Terminal.Reason=model_error (and no ContractFailures), the scheduler
// must (a) populate StepResult.Failures so the wire event has a Reason,
// (b) classify as transient and retry, and (c) record cumulative Attempts.
// Before the fix, this path returned an empty Failures slice, the retry
// classifier saw "no markers" and short-circuited to false, and emit
// step_failed went out empty — the user-reported "no error logs / no
// retry" symptom.
func TestScheduler_TerminalModelErrorRetriesAndSurfacesReason(t *testing.T) {
	disp := &sequenceDispatcher{
		results: []*agent.SpawnResult{
			{
				Status:   "error",
				Terminal: &types.Terminal{Reason: types.TerminalModelError, Message: "upstream 502"},
			},
			{
				Status:             "success",
				Output:             "<summary>recovered</summary>",
				SubmittedArtifacts: []types.ArtifactRef{{ArtifactID: "art_recovered"}},
			},
		},
	}
	sched := NewScheduler(newTestSchedulerDeps(), disp, nil)
	plan := &Plan{
		Goal: "transient terminal",
		Steps: []*PlanStep{
			{ID: "s1", SubagentType: "researcher"},
		},
	}
	res := sched.Run(context.Background(), plan, nil)
	if res.Status != "success" {
		t.Fatalf("expected success after retry; got %q (%s)", res.Status, res.Reason)
	}
	if disp.calls != 2 {
		t.Errorf("terminal_model_error should be classified transient and retried; got %d calls", disp.calls)
	}
	if got := res.StepResults[0].Attempts; got != 2 {
		t.Errorf("expected Attempts=2 after retry on terminal_model_error; got %d", got)
	}
}

func TestScheduler_TerminalBlockingLimitClassifiedRateLimited(t *testing.T) {
	r := &StepResult{
		Status:   "failed",
		Failures: []string{"terminal_blocking_limit: provider quota exhausted"},
	}
	if !isTransientFailure(r) {
		t.Errorf("terminal_blocking_limit should be transient")
	}
	if got := classifyStepErrorType(r.Failures); got != emit.ErrorTypeToolRateLimited {
		t.Errorf("terminal_blocking_limit should classify as %q; got %q",
			emit.ErrorTypeToolRateLimited, got)
	}
}

func TestAppendTerminalFailure(t *testing.T) {
	cases := []struct {
		name string
		res  *agent.SpawnResult
		want []string
	}{
		{"nil result", nil, nil},
		{"nil terminal", &agent.SpawnResult{}, nil},
		{"empty reason", &agent.SpawnResult{Terminal: &types.Terminal{}}, nil},
		{
			"reason only",
			&agent.SpawnResult{Terminal: &types.Terminal{Reason: types.TerminalModelError}},
			[]string{"terminal_model_error"},
		},
		{
			"reason + message",
			&agent.SpawnResult{Terminal: &types.Terminal{
				Reason:  types.TerminalBlockingLimit,
				Message: "credit exhausted",
			}},
			[]string{"terminal_blocking_limit: credit exhausted"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := appendTerminalFailure(nil, tc.res)
			if len(got) != len(tc.want) {
				t.Fatalf("len(appendTerminalFailure)=%d; want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("appendTerminalFailure[%d]=%q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestClassifyStepErrorType_MatchesTransientView pins the invariant that
// every transient marker isTransientFailure recognises also resolves to a
// retryable emit.ErrorType — the two views must agree so wire consumers
// don't see "retryable=true" alongside a permanent ErrorType.
func TestClassifyStepErrorType_MatchesTransientView(t *testing.T) {
	transient := []string{
		"timeout",
		"rate limit",
		"overloaded",
		"503 service",
		"504 gateway",
	}
	for _, msg := range transient {
		t.Run(strings.ReplaceAll(msg, " ", "_"), func(t *testing.T) {
			et := classifyStepErrorType([]string{msg})
			switch et {
			case emit.ErrorTypeToolTimeout,
				emit.ErrorTypeToolRateLimited,
				emit.ErrorTypeOverloaded:
				// ok
			default:
				t.Errorf("transient marker %q mapped to non-transient ErrorType %q",
					msg, et)
			}
		})
	}
}
