package msgbus_test

import (
	"context"
	"testing"
	"time"

	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/msgbus/store"
)

func TestInMemBusPublishSubscribe(t *testing.T) {
	ctx := context.Background()
	b := msgbus.NewInMem(store.NewMemory())
	defer b.Close()

	ch, cancel := b.Subscribe(msgbus.AddrAgent("t-001"))
	defer cancel()

	msg := msgbus.AgentMessage{
		MsgID: "m1", Kind: msgbus.KindNotify, To: msgbus.AddrAgent("t-001"),
		TaskID: "t-001", Ts: time.Now(),
	}
	if err := b.Publish(ctx, msg); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ch:
		if got.MsgID != "m1" {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for msg")
	}
}

func TestInMemBusDequeueByTopic(t *testing.T) {
	ctx := context.Background()
	b := msgbus.NewInMem(store.NewMemory())
	defer b.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = b.Publish(ctx, msgbus.AgentMessage{
			MsgID: "m1", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf"),
			Payload: msgbus.TaskMessage{TaskID: "t-1", TaskType: "leaf", Task: "x"},
		})
	}()
	dctx, dcancel := context.WithTimeout(ctx, time.Second)
	defer dcancel()
	msg, err := b.Dequeue(dctx, "leaf", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if msg.MsgID != "m1" {
		t.Fatalf("got %+v", msg)
	}
	_ = b.Ack(msg.MsgID)
}
