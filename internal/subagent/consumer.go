package subagent

import (
	"context"
	"fmt"
	"time"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
)

// ContextFactory builds a LeafContext for a given task. Provided by scheduler.New.
type ContextFactory interface {
	Build(taskID types.TaskID, sessionID string, sp spec.TaskSpec) LeafContext
}

// ConsumerPool is a pool of goroutines that dequeue L2→L3 task messages,
// run them via Runner, and publish lifecycle + result messages back to L2.
type ConsumerPool struct {
	bus      msgbus.Bus
	reader   tstate.Reader
	factory  ContextFactory
	poolSize int
}

// NewConsumerPool creates a ConsumerPool. poolSize <= 0 defaults to 4.
func NewConsumerPool(bus msgbus.Bus, reader tstate.Reader, factory ContextFactory, poolSize int) *ConsumerPool {
	if poolSize <= 0 {
		poolSize = 4
	}
	return &ConsumerPool{bus: bus, reader: reader, factory: factory, poolSize: poolSize}
}

// Start launches poolSize worker goroutines. They run until ctx is cancelled.
func (p *ConsumerPool) Start(ctx context.Context) {
	for i := 0; i < p.poolSize; i++ {
		consumerID := fmt.Sprintf("consumer:c-%d", i)
		go p.workerLoop(ctx, consumerID)
	}
}

func (p *ConsumerPool) workerLoop(ctx context.Context, consumerID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		msg, err := p.bus.Dequeue(ctx, "leaf", consumerID)
		if err != nil {
			return
		}
		p.handleOne(ctx, msg, consumerID)
	}
}

func (p *ConsumerPool) handleOne(ctx context.Context, msg msgbus.AgentMessage, consumerID string) {
	tm, ok := msg.Payload.(msgbus.TaskMessage)
	if !ok {
		// Malformed payload; ack to discard.
		_ = p.bus.Ack(msg.MsgID)
		return
	}
	taskID := types.TaskID(tm.TaskID)
	task, err := p.reader.Get(ctx, taskID)
	if err != nil {
		_ = p.bus.Ack(msg.MsgID) // task gone; discard
		return
	}
	leafCtx := p.factory.Build(taskID, task.SessionID, task.LeafSpec)

	addr := msgbus.AddrAgent(string(taskID))

	// 2. lifecycle{started}
	if err := p.bus.Publish(ctx, msgbus.AgentMessage{
		MsgID:   fmt.Sprintf("life:%s:start:%d", taskID, task.Attempt),
		Kind:    msgbus.KindLifecycle,
		From:    addr,
		TaskID:  string(taskID),
		Payload: msgbus.LifecyclePayload{Event: msgbus.EventStarted, Attempt: task.Attempt},
	}); err != nil {
		return // don't ack; let reaper handle
	}

	// 3. fork heartbeat
	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go heartbeatLoop(hbCtx, p.bus, taskID, task.Attempt, leaseTTLFor(task))

	// 4. run
	runner := NewRunner(leafCtx, consumerID)
	metaRef, runErr := runner.Run(ctx)
	stopHB()

	// 5. stage (v3.1-R3 stage before publish)
	if runErr == nil {
		if stageErr := leafCtx.Staging.StageResult(ctx, taskID, types.Ref(metaRef), task.Attempt); stageErr != nil {
			return // don't ack
		}
	}

	// 6. lifecycle{completed/failed}
	lcMsg := msgbus.AgentMessage{
		MsgID:  fmt.Sprintf("life:%s:done:%d", taskID, task.Attempt),
		Kind:   msgbus.KindLifecycle,
		From:   addr,
		TaskID: string(taskID),
	}
	if runErr == nil {
		lcMsg.Payload = msgbus.LifecyclePayload{
			Event:     msgbus.EventCompleted,
			Attempt:   task.Attempt,
			ResultRef: string(metaRef),
		}
	} else {
		lcMsg.Payload = msgbus.LifecyclePayload{
			Event:         msgbus.EventFailed,
			Attempt:       task.Attempt,
			FailureReason: string(types.FailWorkerError),
			ErrMsg:        runErr.Error(),
		}
	}
	if err := p.bus.Publish(ctx, lcMsg); err != nil {
		return // don't ack
	}

	// 7. KindResult
	status := msgbus.ResultStatusDone
	reason := ""
	if runErr != nil {
		status = msgbus.ResultStatusFailed
		reason = "worker_error: " + runErr.Error()
	}
	resMsg := msgbus.AgentMessage{
		MsgID:  fmt.Sprintf("result:%s:%d", taskID, task.Attempt),
		Kind:   msgbus.KindResult,
		From:   addr,
		To:     msgbus.AddrScheduler,
		TaskID: string(taskID),
		Payload: msgbus.ResultMessage{
			TaskID:     string(taskID),
			TaskType:   tm.TaskType,
			OutputFile: string(metaRef),
			Status:     status,
			Summary:    task.LeafSpec.Goal,
			Reason:     reason,
		},
	}
	if err := p.bus.Publish(ctx, resMsg); err != nil {
		return // don't ack
	}

	// 8. ack
	_ = p.bus.Ack(msg.MsgID)
}

func heartbeatLoop(ctx context.Context, bus msgbus.Bus, taskID types.TaskID, attempt int, ttl time.Duration) {
	tk := time.NewTicker(ttl / 3)
	defer tk.Stop()
	addr := msgbus.AddrAgent(string(taskID))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			_ = bus.Publish(ctx, msgbus.AgentMessage{
				MsgID:  fmt.Sprintf("life:%s:hb:%d:%d", taskID, attempt, time.Now().UnixNano()),
				Kind:   msgbus.KindLifecycle,
				From:   addr,
				TaskID: string(taskID),
				Payload: msgbus.LifecyclePayload{
					Event:   msgbus.EventHeartbeat,
					Attempt: attempt,
				},
			})
		}
	}
}

func leaseTTLFor(t tstate.TaskState) time.Duration {
	if t.ResourceReq.LeaseTTL > 0 {
		return t.ResourceReq.LeaseTTL
	}
	return 30 * time.Second
}
