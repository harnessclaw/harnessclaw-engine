package emit

import (
	"context"
	"sync"
)

type Memory struct {
	mu     sync.RWMutex
	subs   map[string]map[uint64]Subscriber
	nextID uint64
}

func NewMemory() *Memory {
	return &Memory{subs: make(map[string]map[uint64]Subscriber)}
}

func (b *Memory) Subscribe(topic string, fn Subscriber) (unsubscribe func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	if b.subs[topic] == nil { b.subs[topic] = make(map[uint64]Subscriber) }
	b.subs[topic][id] = fn
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[topic], id)
	}
}

func (b *Memory) Publish(ctx context.Context, evt Event) error {
	b.mu.RLock()
	fns := make([]Subscriber, 0, len(b.subs[evt.Topic]))
	for _, fn := range b.subs[evt.Topic] { fns = append(fns, fn) }
	b.mu.RUnlock()
	for _, fn := range fns {
		go func(f Subscriber) {
			defer func() { recover() }()
			f(ctx, evt)
		}(fn)
	}
	return nil
}

var _ Bus = (*Memory)(nil)
