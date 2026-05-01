package orchestrate

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/pkg/types"
)

// drainEvents collects every event sent to the channel until the executor
// returns. The buffer is generous so emit calls never block the executor.
func drainEvents(out chan types.EngineEvent, done <-chan struct{}) []types.EngineEvent {
	var collected []types.EngineEvent
	for {
		select {
		case evt := <-out:
			collected = append(collected, evt)
		case <-done:
			// Drain any straggler events that may already be in the channel.
			for {
				select {
				case evt := <-out:
					collected = append(collected, evt)
				default:
					return collected
				}
			}
		}
	}
}

func TestExecutor_EmitsPlanAndTaskLifecycle(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s1", "A 完成"), nil
	})
	sp.on("s2", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return successResult("s2", "B 完成"), nil
	})

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "researcher", Task: "查 A"},
		{StepID: "s2", SubagentType: "analyst", Task: "对比", DependsOn: []string{"s1"}},
	}}

	out := make(chan types.EngineEvent, 64)
	done := make(chan struct{})

	exec := NewPlanExecutor(sp, zap.NewNop())
	go func() {
		defer close(done)
		exec.Execute(context.Background(), plan, ExecuteOptions{
			ParentOut:      out,
			TraceID:        "tr_emit_1",
			Sequencer:      emit.NewSequencer(),
			PlanGoal:       "比对营收",
			PlanAgentID:    "plan_agent",
			PlanAgentRunID: "run_plan_1",
		})
	}()

	events := drainEvents(out, done)

	// Group by type for assertions.
	counts := make(map[types.EngineEventType]int)
	for _, e := range events {
		counts[e.Type]++
	}

	if counts[types.EngineEventPlanCreated] != 1 {
		t.Errorf("expected 1 plan_created, got %d", counts[types.EngineEventPlanCreated])
	}
	if counts[types.EngineEventPlanCompleted] != 1 {
		t.Errorf("expected 1 plan_completed, got %d", counts[types.EngineEventPlanCompleted])
	}
	if counts[types.EngineEventStepDispatched] != 2 {
		t.Errorf("expected 2 task_dispatched, got %d", counts[types.EngineEventStepDispatched])
	}
	if counts[types.EngineEventStepCompleted] != 2 {
		t.Errorf("expected 2 task_completed, got %d", counts[types.EngineEventStepCompleted])
	}

	// Every emitted event must carry an envelope with the right trace ID
	// and a unique, monotonically-increasing seq.
	seen := make(map[int64]bool)
	for _, e := range events {
		if e.Envelope == nil {
			t.Errorf("event %s missing envelope", e.Type)
			continue
		}
		if e.Envelope.TraceID != "tr_emit_1" {
			t.Errorf("event %s wrong trace_id: %q", e.Type, e.Envelope.TraceID)
		}
		if e.Envelope.AgentRole != emit.RoleOrchestrator {
			t.Errorf("event %s should be orchestrator, got %q", e.Type, e.Envelope.AgentRole)
		}
		if seen[e.Envelope.Seq] {
			t.Errorf("seq %d issued twice", e.Envelope.Seq)
		}
		seen[e.Envelope.Seq] = true
	}

	// plan_created must include the full task graph.
	for _, e := range events {
		if e.Type == types.EngineEventPlanCreated {
			if e.PlanEvent == nil || len(e.PlanEvent.Tasks) != 2 {
				t.Errorf("plan_created tasks: %+v", e.PlanEvent)
			}
		}
	}
}

func TestExecutor_EmitsTaskFailedAndSkipped(t *testing.T) {
	sp := newRecordingSpawner()
	sp.on("s1", func(_ int, _ *agent.SpawnConfig) (*agent.SpawnResult, error) {
		return nil, errors.New("upstream API down")
	})
	// s2 depends on s1 — should never run, should emit task.skipped.

	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "researcher", Task: "fetch"},
		{StepID: "s2", SubagentType: "analyst", Task: "compare", DependsOn: []string{"s1"}},
	}}

	out := make(chan types.EngineEvent, 64)
	done := make(chan struct{})

	exec := NewPlanExecutor(sp, zap.NewNop())
	go func() {
		defer close(done)
		exec.Execute(context.Background(), plan, ExecuteOptions{
			ParentOut: out,
			TraceID:   "tr_emit_2",
			Sequencer: emit.NewSequencer(),
		})
	}()

	events := drainEvents(out, done)

	counts := make(map[types.EngineEventType]int)
	for _, e := range events {
		counts[e.Type]++
	}
	if counts[types.EngineEventStepFailed] != 1 {
		t.Errorf("expected 1 task_failed, got %d", counts[types.EngineEventStepFailed])
	}
	if counts[types.EngineEventStepSkipped] != 1 {
		t.Errorf("expected 1 task_skipped, got %d", counts[types.EngineEventStepSkipped])
	}

	for _, e := range events {
		if e.Type == types.EngineEventStepFailed {
			if e.TaskDispatch == nil || e.TaskDispatch.Error == "" {
				t.Errorf("task_failed missing error: %+v", e.TaskDispatch)
			}
			if e.Envelope == nil || e.Envelope.Severity != emit.SeverityError {
				t.Errorf("task_failed should be error severity")
			}
		}
		if e.Type == types.EngineEventStepSkipped {
			if e.TaskDispatch == nil || e.TaskDispatch.Reason == "" {
				t.Errorf("task_skipped missing reason: %+v", e.TaskDispatch)
			}
		}
	}
}

func TestExecutor_NoEmitWhenSequencerNil(t *testing.T) {
	sp := newRecordingSpawner()
	plan := &Plan{Steps: []Step{
		{StepID: "s1", SubagentType: "writer", Task: "x"},
	}}

	out := make(chan types.EngineEvent, 16)
	done := make(chan struct{})

	exec := NewPlanExecutor(sp, zap.NewNop())
	go func() {
		defer close(done)
		// ParentOut wired but Sequencer nil → emit should be a no-op.
		exec.Execute(context.Background(), plan, ExecuteOptions{ParentOut: out})
	}()

	events := drainEvents(out, done)
	for _, e := range events {
		if e.Type == types.EngineEventPlanCreated || e.Type == types.EngineEventStepDispatched {
			t.Errorf("unexpected emit event without Sequencer: %s", e.Type)
		}
	}
}
