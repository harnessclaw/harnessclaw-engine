package dispatch_test

import (
	"context"
	"testing"

	"harnessclaw-go/internal/engine/agent/scheduler/dispatch"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
)

// Stub implementations for compile-check.
var (
	_ dispatch.Strategy = (*stubStrategy)(nil)
	_ tstate.Reader     = (*stubReader)(nil)
	_ msgbus.Bus        = (*stubBus)(nil)
	_ tstate.StagingWriter = (*stubStaging)(nil)
)

type stubStrategy struct{}

func (s *stubStrategy) Kind() types.Kind {
	return "stub"
}

func (s *stubStrategy) Capabilities() dispatch.Capabilities {
	return dispatch.Capabilities{
		AllowedTools:  []string{"Read"},
		AllowSubmit:   false,
		MaxSpawnDepth: 1,
		LeafKind:      "react-leaf",
		IdempotentRun: true,
	}
}

func (s *stubStrategy) Run(ctx context.Context, taskID types.TaskID, deps dispatch.Deps) (types.MetaRef, error) {
	return "meta.json", nil
}

type stubReader struct{}

func (sr *stubReader) Get(ctx context.Context, id types.TaskID) (tstate.TaskState, error) {
	return tstate.TaskState{}, nil
}

func (sr *stubReader) ListReady(ctx context.Context, team types.TeamID, limit int) ([]tstate.TaskState, error) {
	return nil, nil
}

func (sr *stubReader) ListChildren(ctx context.Context, parent types.TaskID) ([]tstate.TaskState, error) {
	return nil, nil
}

func (sr *stubReader) ListByStatus(ctx context.Context, team types.TeamID, st types.Status, limit int) ([]tstate.TaskState, error) {
	return nil, nil
}

func (sr *stubReader) ListPendingDependentOn(ctx context.Context, depID types.TaskID) ([]tstate.TaskState, error) {
	return nil, nil
}

type stubBus struct{}

func (sb *stubBus) Publish(ctx context.Context, msg msgbus.AgentMessage) error {
	return nil
}

func (sb *stubBus) Subscribe(to msgbus.Address) (<-chan msgbus.AgentMessage, msgbus.Cancel) {
	return nil, nil
}

func (sb *stubBus) SubscribeOnce(filters ...any) (<-chan msgbus.AgentMessage, msgbus.Cancel) {
	return nil, nil
}

func (sb *stubBus) Dequeue(ctx context.Context, topic string, consumerID string) (msgbus.AgentMessage, error) {
	return msgbus.AgentMessage{}, nil
}

func (sb *stubBus) Ack(msgID string) error {
	return nil
}

func (sb *stubBus) Nack(msgID string, retry bool) error {
	return nil
}

func (sb *stubBus) Query() msgbus.BusQuery {
	return nil
}

type stubStaging struct{}

func (ss *stubStaging) StageResult(ctx context.Context, id types.TaskID, ref types.Ref, attempt int) error {
	return nil
}

// TestStrategyInterface verifies that stub implementations compile against Strategy.
func TestStrategyInterface(t *testing.T) {
	stub := &stubStrategy{}
	if stub.Kind() != "stub" {
		t.Fatalf("unexpected kind")
	}
	caps := stub.Capabilities()
	if caps.MaxSpawnDepth != 1 {
		t.Fatalf("unexpected MaxSpawnDepth")
	}
}

// TestDepsStructure verifies Deps compiles with all fields.
func TestDepsStructure(t *testing.T) {
	deps := dispatch.Deps{
		Reader:  &stubReader{},
		Bus:     &stubBus{},
		Staging: &stubStaging{},
	}
	if deps.Reader == nil || deps.Bus == nil || deps.Staging == nil {
		t.Fatalf("Deps not fully initialized")
	}
}
