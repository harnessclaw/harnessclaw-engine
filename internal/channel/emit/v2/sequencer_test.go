package emitv2

import (
	"sync"
	"testing"
)

func TestSequencer_NextStartsAtOne(t *testing.T) {
	s := NewSequencer()
	if got := s.Next("tr_a"); got != 1 {
		t.Errorf("first Next = %d, want 1", got)
	}
}

func TestSequencer_PerTraceMonotonic(t *testing.T) {
	s := NewSequencer()
	for i := int64(1); i <= 100; i++ {
		if got := s.Next("tr_a"); got != i {
			t.Fatalf("Next #%d = %d", i, got)
		}
	}
}

func TestSequencer_TracesAreIsolated(t *testing.T) {
	s := NewSequencer()
	s.Next("tr_a")
	s.Next("tr_a")
	if got := s.Next("tr_b"); got != 1 {
		t.Errorf("tr_b first Next = %d, want 1 (independent counter)", got)
	}
}

func TestSequencer_DropResetsCounter(t *testing.T) {
	s := NewSequencer()
	s.Next("tr_a")
	s.Next("tr_a")
	s.Drop("tr_a")
	if got := s.Next("tr_a"); got != 1 {
		t.Errorf("after Drop, Next = %d, want 1", got)
	}
}

func TestSequencer_PeekDoesNotIncrement(t *testing.T) {
	s := NewSequencer()
	s.Next("tr_a")
	s.Next("tr_a")
	if got := s.Peek("tr_a"); got != 2 {
		t.Errorf("Peek = %d, want 2", got)
	}
	if got := s.Next("tr_a"); got != 3 {
		t.Errorf("after Peek, Next = %d, want 3", got)
	}
}

func TestSequencer_PeekUnknownTrace(t *testing.T) {
	s := NewSequencer()
	if got := s.Peek("missing"); got != 0 {
		t.Errorf("Peek(missing) = %d, want 0", got)
	}
}

func TestSequencer_ConcurrentNext(t *testing.T) {
	s := NewSequencer()
	const N = 1000
	var wg sync.WaitGroup
	seen := make([]int64, 0, N)
	var mu sync.Mutex

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := s.Next("tr_x")
			mu.Lock()
			seen = append(seen, n)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(seen) != N {
		t.Fatalf("got %d allocations, want %d", len(seen), N)
	}
	// Check uniqueness — concurrent allocation must not collide.
	bag := make(map[int64]struct{}, N)
	for _, n := range seen {
		if _, dup := bag[n]; dup {
			t.Fatalf("duplicate seq %d under concurrent Next", n)
		}
		bag[n] = struct{}{}
		if n < 1 || n > N {
			t.Fatalf("seq %d out of expected range [1, %d]", n, N)
		}
	}
}
