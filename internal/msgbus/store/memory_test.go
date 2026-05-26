// internal/msgbus/store/memory_test.go
package store_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/msgbus/store"
)

func TestMemoryEnqueueDequeueAck(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	defer s.Close()

	msg := msgbus.AgentMessage{
		MsgID: "m1", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf"),
		TaskID: "t-1", Ts: time.Now(),
	}
	if err := s.Enqueue(ctx, msg); err != nil {
		t.Fatal(err)
	}

	got, err := s.Dequeue(ctx, "leaf", "c-1")
	if err != nil || got.MsgID != "m1" {
		t.Fatalf("Dequeue: got=%+v err=%v", got, err)
	}

	_, st, meta, _ := s.GetMessage(ctx, "m1")
	if st != msgbus.MsgDelivered || meta.DeliveredTo != "c-1" {
		t.Fatalf("delivered metadata wrong: %v %+v", st, meta)
	}

	if err := s.Ack(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	_, st, _, _ = s.GetMessage(ctx, "m1")
	if st != msgbus.MsgAcked {
		t.Fatalf("want acked, got %s", st)
	}
}

func TestMemoryReaperRequeue(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	defer s.Close()

	msg := msgbus.AgentMessage{MsgID: "m2", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf")}
	_ = s.Enqueue(ctx, msg)
	_, _ = s.Dequeue(ctx, "leaf", "c-1")

	// Simulate ack timeout
	expired, _ := s.Reaper(ctx, time.Now().Add(1*time.Hour), 30*time.Second)
	if len(expired) != 1 {
		t.Fatalf("want 1 expired, got %d", len(expired))
	}
}

func TestMemoryListByTaskID(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	defer s.Close()
	_ = s.Enqueue(ctx, msgbus.AgentMessage{MsgID: "m1", Kind: msgbus.KindTask, TaskID: "t-1", To: msgbus.AddrQueue("leaf")})
	_ = s.Enqueue(ctx, msgbus.AgentMessage{MsgID: "m2", Kind: msgbus.KindResult, TaskID: "t-1"})
	_ = s.Enqueue(ctx, msgbus.AgentMessage{MsgID: "m3", Kind: msgbus.KindTask, TaskID: "t-2", To: msgbus.AddrQueue("leaf")})
	rs, _ := s.ListByTaskID(ctx, "t-1")
	if len(rs) != 2 {
		t.Fatalf("want 2 for t-1, got %d", len(rs))
	}
}
