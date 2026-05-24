// internal/msgbus/store/sqlite_test.go
package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/msgbus/store"
	sqlitepkg "harnessclaw-go/internal/storage/sqlite"
)

func newSQLiteForTest(t *testing.T) *store.SQLite {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "msgbus_test.db")
	base, err := sqlitepkg.New(dbPath)
	if err != nil { t.Fatal(err) }
	s, err := store.NewSQLite(base.DB())
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close(); base.Close() })
	return s
}

func TestSQLiteEnqueueDequeueAck(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteForTest(t)
	msg := msgbus.AgentMessage{
		MsgID: "m1", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf"),
		TaskID: "t-1", Ts: time.Now(),
		Payload: msgbus.TaskMessage{TaskID: "t-1", TaskType: "leaf", Task: "x"},
	}
	if err := s.Enqueue(ctx, msg); err != nil { t.Fatal(err) }
	got, err := s.Dequeue(ctx, "leaf", "c-1")
	if err != nil || got.MsgID != "m1" {
		t.Fatalf("dequeue: %+v err=%v", got, err)
	}
	if err := s.Ack(ctx, "m1"); err != nil { t.Fatal(err) }
	_, st, _, _ := s.GetMessage(ctx, "m1")
	if st != msgbus.MsgAcked { t.Fatalf("want acked, got %s", st) }
}

func TestSQLiteSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "msgbus_restart.db")

	{
		base, err := sqlitepkg.New(dbPath)
		if err != nil { t.Fatal(err) }
		s, err := store.NewSQLite(base.DB())
		if err != nil { t.Fatal(err) }
		_ = s.Enqueue(ctx, msgbus.AgentMessage{
			MsgID: "m2", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf"),
			TaskID: "t-2", Ts: time.Now(),
			Payload: msgbus.TaskMessage{TaskID: "t-2", TaskType: "leaf", Task: "y"},
		})
		s.Close(); base.Close()
	}

	// Reopen DB and verify message survives
	base, _ := sqlitepkg.New(dbPath)
	defer base.Close()
	s, _ := store.NewSQLite(base.DB())
	defer s.Close()
	got, err := s.Dequeue(ctx, "leaf", "c-2")
	if err != nil || got.MsgID != "m2" {
		t.Fatalf("restart dequeue: %+v err=%v", got, err)
	}
}

func TestSQLiteNackRetry(t *testing.T) {
	ctx := context.Background()
	s := newSQLiteForTest(t)
	_ = s.Enqueue(ctx, msgbus.AgentMessage{
		MsgID: "m3", Kind: msgbus.KindTask, To: msgbus.AddrQueue("leaf"),
		TaskID: "t-3", Ts: time.Now(),
	})
	_, _ = s.Dequeue(ctx, "leaf", "c-1")
	if err := s.Nack(ctx, "m3", true); err != nil { t.Fatal(err) }
	// should be requeued; new Dequeue should pull it again
	got, err := s.Dequeue(ctx, "leaf", "c-2")
	if err != nil || got.MsgID != "m3" {
		t.Fatalf("nack retry failed to requeue: %+v err=%v", got, err)
	}
}
