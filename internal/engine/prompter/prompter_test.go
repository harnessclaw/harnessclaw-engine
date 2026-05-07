package prompter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"harnessclaw-go/internal/engine/wait"
)

// memStore is a thread-safe in-memory wait.Store for tests. The SQLite
// impl has its own tests; this isolates the Prompter logic.
type memStore struct {
	mu   sync.Mutex
	rows map[string]wait.PendingWait
	fail bool // when true, Save returns an error
}

func newMemStore() *memStore { return &memStore{rows: make(map[string]wait.PendingWait)} }

func (s *memStore) Save(_ context.Context, w wait.PendingWait) error {
	if s.fail {
		return errors.New("simulated save failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[w.RequestID] = w
	return nil
}

func (s *memStore) Get(_ context.Context, id string) (*wait.PendingWait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.rows[id]; ok {
		return &w, nil
	}
	return nil, nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.rows, id)
	s.mu.Unlock()
	return nil
}

func (s *memStore) ListBySession(_ context.Context, sid string) ([]*wait.PendingWait, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*wait.PendingWait, 0)
	for _, w := range s.rows {
		if w.SessionID == sid {
			ww := w
			out = append(out, &ww)
		}
	}
	return out, nil
}

func (s *memStore) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, w := range s.rows {
		if !w.ExpiresAt.IsZero() && !w.ExpiresAt.After(now) {
			delete(s.rows, id)
			n++
		}
	}
	return n, nil
}

func sampleWait(id string) wait.PendingWait {
	return wait.PendingWait{
		RequestID:     id,
		SessionID:     "sess_x",
		Kind:          wait.KindQuestion,
		CorrelationID: "toolu_y",
	}
}

func TestPrompter_Issue_PersistsBeforeReturning(t *testing.T) {
	store := newMemStore()
	p := New(Config{Store: store})

	w := sampleWait("req_1")
	_, done, err := p.Issue(context.Background(), w)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	defer done()

	got, _ := store.Get(context.Background(), "req_1")
	if got == nil {
		t.Fatal("wait was not persisted before Issue returned")
	}
	if got.SessionID != "sess_x" {
		t.Errorf("persisted wait wrong: %+v", got)
	}
}

func TestPrompter_Issue_FailsWhenStoreFails(t *testing.T) {
	store := &memStore{rows: map[string]wait.PendingWait{}, fail: true}
	p := New(Config{Store: store})
	_, _, err := p.Issue(context.Background(), sampleWait("req_2"))
	if err == nil {
		t.Fatal("expected Issue to fail when store fails (don't emit unrecoverable prompts)")
	}
}

func TestPrompter_Issue_AutoExpiry(t *testing.T) {
	store := newMemStore()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	p := New(Config{Store: store, Now: func() time.Time { return now }})

	_, done, _ := p.Issue(context.Background(), sampleWait("req_e"))
	defer done()

	got, _ := store.Get(context.Background(), "req_e")
	if got.CreatedAt != now {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
	if got.ExpiresAt != now.Add(wait.DefaultExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, now.Add(wait.DefaultExpiry))
	}
}

func TestPrompter_DeliverLive(t *testing.T) {
	store := newMemStore()
	p := New(Config{Store: store})

	ch, done, _ := p.Issue(context.Background(), sampleWait("req_live"))
	defer done()

	go func() {
		time.Sleep(10 * time.Millisecond)
		if !p.Deliver("req_live", wait.Answer{Decision: "approved", Output: "red"}) {
			t.Errorf("Deliver returned false, want true (live waiter present)")
		}
	}()

	select {
	case ans := <-ch:
		if ans.Output != "red" {
			t.Errorf("answer.Output = %q", ans.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("Deliver did not unblock waiter")
	}
}

func TestPrompter_DeliverNoLive(t *testing.T) {
	p := New(Config{Store: newMemStore()})
	if p.Deliver("req_unknown", wait.Answer{}) {
		t.Error("Deliver should return false for unknown request_id (no live waiter)")
	}
}

func TestPrompter_DeliverAfterUnregister(t *testing.T) {
	p := New(Config{Store: newMemStore()})
	_, done, _ := p.Issue(context.Background(), sampleWait("req_gone"))
	done() // simulate engine goroutine exited (e.g. ctx cancelled)
	if p.Deliver("req_gone", wait.Answer{}) {
		t.Error("Deliver should return false after unregister (recovery path needed)")
	}
}

func TestPrompter_LookupAfterRestart(t *testing.T) {
	// Simulate a "restart": create Prompter A, Issue, then create
	// Prompter B over the SAME store and look up the wait. Recovery
	// path goes through Lookup, not the live registry.
	store := newMemStore()

	pA := New(Config{Store: store})
	_, doneA, _ := pA.Issue(context.Background(), sampleWait("req_recover"))
	doneA() // a's live registry forgets, but store row remains

	pB := New(Config{Store: store}) // fresh registry — simulates restart
	got, err := pB.Lookup(context.Background(), "req_recover")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil after restart")
	}
	if got.SessionID != "sess_x" {
		t.Errorf("recovered wait corrupted: %+v", got)
	}
	// And there's no live waiter on B.
	if pB.Deliver("req_recover", wait.Answer{}) {
		t.Error("fresh Prompter should not have a live waiter")
	}
}

func TestPrompter_ListSession(t *testing.T) {
	p := New(Config{Store: newMemStore()})
	w1 := sampleWait("req_l1")
	w2 := sampleWait("req_l2")
	w3 := sampleWait("req_other")
	w3.SessionID = "sess_other"
	for _, w := range []wait.PendingWait{w1, w2, w3} {
		_, done, _ := p.Issue(context.Background(), w)
		done()
	}
	got, _ := p.ListSession(context.Background(), "sess_x")
	if len(got) != 2 {
		t.Errorf("ListSession returned %d, want 2", len(got))
	}
}

func TestPrompter_Forget(t *testing.T) {
	store := newMemStore()
	p := New(Config{Store: store})
	_, done, _ := p.Issue(context.Background(), sampleWait("req_f"))
	done()
	if err := p.Forget(context.Background(), "req_f"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	got, _ := store.Get(context.Background(), "req_f")
	if got != nil {
		t.Error("Forget did not remove from store")
	}
}

func TestPrompter_SweepExpired(t *testing.T) {
	store := newMemStore()
	now := time.Now()
	p := New(Config{Store: store, Now: func() time.Time { return now }})

	w1 := sampleWait("req_old")
	w1.ExpiresAt = now.Add(-time.Hour)
	_ = store.Save(context.Background(), w1)

	w2 := sampleWait("req_new")
	w2.ExpiresAt = now.Add(time.Hour)
	_ = store.Save(context.Background(), w2)

	n, err := p.SweepExpired(context.Background())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("SweepExpired returned %d, want 1", n)
	}
}

func TestPrompter_ConcurrentDeliver(t *testing.T) {
	p := New(Config{Store: newMemStore()})
	const N = 100

	// Fan out N concurrent Issue + Deliver pairs. Each pair must
	// independently succeed without races.
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "req_c_" + string(rune('a'+n%26)) + string(rune('0'+n/26))
			ch, done, err := p.Issue(context.Background(), sampleWait(id))
			if err != nil {
				t.Errorf("Issue: %v", err)
				return
			}
			defer done()
			go p.Deliver(id, wait.Answer{Output: id})
			select {
			case ans := <-ch:
				if ans.Output != id {
					t.Errorf("answer mismatch: %s vs %s", ans.Output, id)
				}
			case <-time.After(time.Second):
				t.Errorf("timeout for %s", id)
			}
		}(i)
	}
	wg.Wait()
}
