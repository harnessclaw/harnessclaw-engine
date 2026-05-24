package msgbus

import (
	"context"
	"sync"
)

// Store is copied here to avoid import cycle with msgbus/store.
// The actual Store interface lives in msgbus/store; this is just the contract.
type storeI interface {
	Enqueue(ctx context.Context, msg AgentMessage) error
	Dequeue(ctx context.Context, topic, consumerID string) (AgentMessage, error)
	Ack(ctx context.Context, msgID string) error
	Nack(ctx context.Context, msgID string, retry bool) error
	GetMessage(ctx context.Context, msgID string) (AgentMessage, MsgStatus, MessageMeta, error)
	ListByStatus(ctx context.Context, status MsgStatus, kind Kind, limit int) ([]AgentMessage, error)
	ListByTaskID(ctx context.Context, taskID string) ([]MessageRecord, error)
	QueueStats(ctx context.Context) ([]QueueStat, error)
	Close() error
}

// NewInMem builds an in-process Bus backed by the given Store. Store may be
// store.Memory (phase 1) or sqlite (phase 1 末).
func NewInMem(s storeI) *InMemBus {
	return &InMemBus{
		store: s,
		subs:  map[Address][]*subscription{},
		seen:  map[string]bool{},
	}
}

type subscription struct {
	ch     chan AgentMessage
	filter *SubscribeFilter
	once   bool
}

type InMemBus struct {
	store storeI
	mu    sync.Mutex
	subs  map[Address][]*subscription
	seen  map[string]bool
}

func (b *InMemBus) Close() error { return b.store.Close() }

func (b *InMemBus) Publish(ctx context.Context, msg AgentMessage) error {
	b.mu.Lock()
	if b.seen[msg.MsgID] {
		b.mu.Unlock()
		return nil // dedup
	}
	b.seen[msg.MsgID] = true
	b.mu.Unlock()

	if err := b.store.Enqueue(ctx, msg); err != nil {
		return err
	}

	// Fan out to in-process subscribers (non-queue addresses)
	if _, isQueue := msg.To.QueueName(); !isQueue {
		b.fanout(msg)
	}
	return nil
}

func (b *InMemBus) fanout(msg AgentMessage) {
	b.mu.Lock()
	subs := append([]*subscription(nil), b.subs[msg.To]...)
	// Also fan out to scheduler subscribers (catchall for notify/result routing)
	if msg.To != AddrScheduler {
		subs = append(subs, b.subs[AddrScheduler]...)
	}
	b.mu.Unlock()

	for _, s := range subs {
		if s.filter != nil && !filterMatch(s.filter, msg) {
			continue
		}
		select {
		case s.ch <- msg:
		default:
		}
		if s.once {
			b.unsubscribeAll(s)
		}
	}
}

func filterMatch(f *SubscribeFilter, msg AgentMessage) bool {
	if f.Kind != "" && msg.Kind != f.Kind {
		return false
	}
	if f.TaskID != "" && msg.TaskID != f.TaskID {
		return false
	}
	if f.Notify != "" {
		p, ok := msg.Payload.(NotifyPayload)
		if !ok || p.Event != f.Notify {
			return false
		}
	}
	return true
}

func (b *InMemBus) Subscribe(to Address) (<-chan AgentMessage, Cancel) {
	s := &subscription{ch: make(chan AgentMessage, 64)}
	b.mu.Lock()
	b.subs[to] = append(b.subs[to], s)
	b.mu.Unlock()
	return s.ch, func() { b.unsubscribe(to, s) }
}

func (b *InMemBus) SubscribeOnce(filters ...any) (<-chan AgentMessage, Cancel) {
	f := &SubscribeFilter{}
	var taskAddr Address = AddrScheduler
	for _, x := range filters {
		switch v := x.(type) {
		case SubscribeFilter:
			*f = v
			if v.TaskID != "" {
				taskAddr = AddrAgent(v.TaskID)
			}
		case Kind:
			f.Kind = v
		case string:
			f.TaskID = v
			taskAddr = AddrAgent(v)
		case NotifyEvent:
			f.Notify = v
		}
	}
	s := &subscription{ch: make(chan AgentMessage, 1), filter: f, once: true}
	b.mu.Lock()
	b.subs[taskAddr] = append(b.subs[taskAddr], s)
	if taskAddr != AddrScheduler {
		b.subs[AddrScheduler] = append(b.subs[AddrScheduler], s)
	}
	b.mu.Unlock()
	return s.ch, func() { b.unsubscribeAll(s) }
}

func (b *InMemBus) unsubscribe(to Address, target *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subs[to]
	for i, s := range subs {
		if s == target {
			b.subs[to] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

func (b *InMemBus) unsubscribeAll(target *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for addr, subs := range b.subs {
		for i, s := range subs {
			if s == target {
				b.subs[addr] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
}

func (b *InMemBus) Dequeue(ctx context.Context, topic, consumerID string) (AgentMessage, error) {
	return b.store.Dequeue(ctx, topic, consumerID)
}

func (b *InMemBus) Ack(msgID string) error { return b.store.Ack(context.Background(), msgID) }

func (b *InMemBus) Nack(msgID string, retry bool) error {
	return b.store.Nack(context.Background(), msgID, retry)
}

func (b *InMemBus) Query() BusQuery { return &busQuery{s: b.store} }

type busQuery struct{ s storeI }

func (q *busQuery) GetMessage(msgID string) (AgentMessage, MsgStatus, MessageMeta, error) {
	return q.s.GetMessage(context.Background(), msgID)
}

func (q *busQuery) ListByStatus(st MsgStatus, k Kind, lim int) ([]AgentMessage, error) {
	return q.s.ListByStatus(context.Background(), st, k, lim)
}

func (q *busQuery) ListByTaskID(tid string) ([]MessageRecord, error) {
	return q.s.ListByTaskID(context.Background(), tid)
}

func (q *busQuery) QueueStats() []QueueStat {
	out, _ := q.s.QueueStats(context.Background())
	return out
}
