package emit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemory_PublishSubscribe(t *testing.T) {
	bus := NewMemory()
	var received atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)
	bus.Subscribe("a", func(ctx context.Context, evt Event) {
		received.Add(1); wg.Done()
	})
	bus.Subscribe("a", func(ctx context.Context, evt Event) {
		received.Add(1); wg.Done()
	})
	_ = bus.Publish(context.Background(), Event{Topic: "a", Payload: "x"})
	wg.Wait()
	if received.Load() != 2 { t.Errorf("got %d want 2", received.Load()) }
}

func TestMemory_Unsubscribe(t *testing.T) {
	bus := NewMemory()
	var received atomic.Int32
	unsub := bus.Subscribe("a", func(ctx context.Context, _ Event) { received.Add(1) })
	unsub()
	_ = bus.Publish(context.Background(), Event{Topic: "a"})
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 0 { t.Errorf("got %d want 0 after unsubscribe", received.Load()) }
}

func TestMemory_PanicDoesNotPropagate(t *testing.T) {
	bus := NewMemory()
	bus.Subscribe("a", func(ctx context.Context, _ Event) { panic("boom") })
	// 不能 panic 出来
	_ = bus.Publish(context.Background(), Event{Topic: "a"})
	time.Sleep(10 * time.Millisecond)
}
