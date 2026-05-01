package emit

import (
	"sync"
	"testing"
)

func TestSequencerMonotonic(t *testing.T) {
	s := NewSequencer()
	want := int64(1)
	for i := 0; i < 5; i++ {
		got := s.Next("tr_1")
		if got != want {
			t.Fatalf("seq #%d: got %d, want %d", i, got, want)
		}
		want++
	}
}

func TestSequencerIsolatesTraces(t *testing.T) {
	s := NewSequencer()
	if got := s.Next("tr_a"); got != 1 {
		t.Fatalf("trace a first seq: got %d, want 1", got)
	}
	if got := s.Next("tr_b"); got != 1 {
		t.Fatalf("trace b first seq: got %d, want 1", got)
	}
	if got := s.Next("tr_a"); got != 2 {
		t.Fatalf("trace a second seq: got %d, want 2", got)
	}
}

func TestSequencerConcurrent(t *testing.T) {
	s := NewSequencer()
	const goroutines = 8
	const each = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	results := make(chan int64, goroutines*each)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				results <- s.Next("tr_x")
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[int64]bool, goroutines*each)
	for v := range results {
		if seen[v] {
			t.Fatalf("seq %d issued twice", v)
		}
		seen[v] = true
	}
	if len(seen) != goroutines*each {
		t.Fatalf("expected %d unique seqs, got %d", goroutines*each, len(seen))
	}
}

func TestSequencerDrop(t *testing.T) {
	s := NewSequencer()
	s.Next("tr_drop")
	s.Next("tr_drop")
	s.Drop("tr_drop")
	if got := s.Next("tr_drop"); got != 1 {
		t.Fatalf("after drop, seq should restart at 1, got %d", got)
	}
}

func TestNewIDs(t *testing.T) {
	if id := NewEventID(); len(id) <= len("evt_") {
		t.Fatalf("event id too short: %q", id)
	}
	if id := NewTraceID(); len(id) <= len("tr_") {
		t.Fatalf("trace id too short: %q", id)
	}
	if id := NewAgentRunID(); len(id) <= len("run_") {
		t.Fatalf("agent run id too short: %q", id)
	}
}

func TestAgentRoleEnum(t *testing.T) {
	roles := []AgentRole{RolePersona, RoleOrchestrator, RoleWorker, RoleSystem}
	want := []string{"persona", "orchestrator", "worker", "system"}
	for i, r := range roles {
		if string(r) != want[i] {
			t.Errorf("role %d: got %q want %q", i, r, want[i])
		}
	}
}

func TestErrorTypeShared(t *testing.T) {
	// Sanity check: the §6.12 connection-level types and the new
	// emit-specific types coexist in one enum so monitoring rules can
	// match across both channels.
	cases := map[ErrorType]string{
		ErrorTypeRateLimit:     "rate_limit_error",
		ErrorTypeToolTimeout:   "tool_timeout",
		ErrorTypeOrphanTimeout: "orphan_timeout",
		ErrorTypeAborted:       "aborted",
	}
	for k, v := range cases {
		if string(k) != v {
			t.Errorf("ErrorType %q wire form %q", k, v)
		}
	}
}
