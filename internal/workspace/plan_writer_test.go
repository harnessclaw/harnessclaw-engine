package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestPlanWriter_ConcurrentMutationsAreSerialised(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	reg := NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())

	// Fire 100 concurrent mutations each appending a unique task. The single-
	// consumer guarantee means the final plan has all 100 keys.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := reg.Get(sid).Apply(context.Background(), func(p *Plan) error {
				id := fmt.Sprintf("t_%03d", i)
				p.Tasks[id] = &Task{
					Title:   id,
					Agent:   "x",
					Status:  StatusPending,
					Attempt: 1,
				}
				return nil
			})
			if err != nil {
				t.Errorf("apply %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(PlanPath(root, sid))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Tasks) != 100 {
		t.Errorf("tasks count = %d, want 100", len(p.Tasks))
	}
}

func TestPlanWriter_IdleTimeoutReclaim(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	reg := newPlanWriterRegistryWithIdle(root, 50*time.Millisecond)
	defer reg.StopAll(context.Background())

	w1 := reg.Get(sid)
	if err := w1.Apply(context.Background(), func(p *Plan) error { return nil }); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Wait past idle window so the consumer goroutine reclaims.
	time.Sleep(150 * time.Millisecond)

	// Second access must work — lazy restart.
	w2 := reg.Get(sid)
	if err := w2.Apply(context.Background(), func(p *Plan) error {
		p.Tasks["t_001"] = &Task{Title: "y", Agent: "z", Status: StatusPending, Attempt: 1}
		return nil
	}); err != nil {
		t.Fatalf("second apply: %v", err)
	}
}

func TestPlanWriter_ValidationFailureRollbacksDisk(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	reg := NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())

	// First write some valid state.
	if err := reg.Get(sid).Apply(context.Background(), func(p *Plan) error {
		p.Tasks["t_001"] = &Task{Title: "x", Agent: "y", Status: StatusPending, Attempt: 1}
		return nil
	}); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Mutation that produces an invalid Plan (status enum bad).
	err := reg.Get(sid).Apply(context.Background(), func(p *Plan) error {
		p.Tasks["t_001"].Status = "garbage"
		return nil
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}

	// Disk must still show old valid state.
	b, _ := os.ReadFile(PlanPath(root, sid))
	var p Plan
	_ = json.Unmarshal(b, &p)
	if p.Tasks["t_001"].Status != StatusPending {
		t.Errorf("disk status = %q, want pending (rollback failed)", p.Tasks["t_001"].Status)
	}
}

func TestPlanWriter_ApplyFuncErrorPropagated(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	_ = EnsureSession(root, sid)
	reg := NewPlanWriterRegistry(root)
	defer reg.StopAll(context.Background())

	want := "my mutation rejected"
	err := reg.Get(sid).Apply(context.Background(), func(p *Plan) error {
		return fmt.Errorf("%s", want)
	})
	if err == nil {
		t.Fatalf("expected apply func error to propagate")
	}
	// Must mention the original error so caller can diagnose
	if !contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), want)
	}
}

func TestPlanWriter_StopAllPreventsFurtherApply(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	_ = EnsureSession(root, sid)
	reg := NewPlanWriterRegistry(root)

	w := reg.Get(sid)
	reg.StopAll(context.Background())

	err := w.Apply(context.Background(), func(p *Plan) error { return nil })
	if err == nil {
		t.Errorf("expected error from Apply after StopAll")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (indexOf(s, sub) >= 0))
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
