package middlewares

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/emit"
)

func TestAnalytics_EmitsStartedAndCompleted(t *testing.T) {
	bus := emit.NewMemory()
	var mu sync.Mutex
	var topics []string
	bus.Subscribe(scheduler.TopicSpawnStarted, func(_ context.Context, e emit.Event) {
		mu.Lock(); topics = append(topics, e.Topic); mu.Unlock()
	})
	bus.Subscribe(scheduler.TopicSpawnCompleted, func(_ context.Context, e emit.Event) {
		mu.Lock(); topics = append(topics, e.Topic); mu.Unlock()
	})

	mw := Analytics{Bus: bus}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Strategy: "async", Bag: map[string]any{}}
	ctx, err := mw.Before(context.Background(), scheduler.SpawnParams{}, st)
	if err != nil {
		t.Fatal(err)
	}
	mw.After(ctx, scheduler.SpawnParams{}, st, scheduler.Result{Status: scheduler.StatusAsyncLaunched}, nil)

	time.Sleep(100 * time.Millisecond) // bus 异步派发
	mu.Lock()
	defer mu.Unlock()
	if len(topics) != 2 {
		t.Errorf("expected 2 topics, got %v", topics)
	}
	// Check both topics are present (order may vary due to async dispatch)
	hasStarted := false
	hasCompleted := false
	for _, topic := range topics {
		if topic == scheduler.TopicSpawnStarted {
			hasStarted = true
		}
		if topic == scheduler.TopicSpawnCompleted {
			hasCompleted = true
		}
	}
	if !hasStarted || !hasCompleted {
		t.Errorf("missing topics; got %v", topics)
	}
}

func TestAnalytics_FailedTopicOnError(t *testing.T) {
	bus := emit.NewMemory()
	got := make(chan string, 1)
	bus.Subscribe(scheduler.TopicSpawnFailed, func(_ context.Context, e emit.Event) { got <- e.Topic })

	mw := Analytics{Bus: bus}
	st := &scheduler.SpawnState{AgentID: "a-1", TaskID: "t-1", Bag: map[string]any{}}
	ctx, _ := mw.Before(context.Background(), scheduler.SpawnParams{}, st)
	mw.After(ctx, scheduler.SpawnParams{}, st, scheduler.Result{}, errors.New("boom"))

	select {
	case topic := <-got:
		if topic != scheduler.TopicSpawnFailed {
			t.Errorf("got %q", topic)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("never got failed topic")
	}
}
