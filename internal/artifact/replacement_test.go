package artifact

import (
	"sync"
	"testing"
)

func TestDecideNewEntry(t *testing.T) {
	rs := NewReplacementState()
	ok := rs.Decide("tu_1", "replacement text")
	if !ok {
		t.Error("first Decide should return true")
	}
}

func TestDecideFrozen(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "replacement")

	// Second call for same ID should return false (frozen).
	ok := rs.Decide("tu_1", "different replacement")
	if ok {
		t.Error("second Decide for same ID should return false")
	}

	// Original decision should be preserved.
	text, replaced := rs.IsReplaced("tu_1")
	if !replaced {
		t.Error("tu_1 should be marked as replaced")
	}
	if text != "replacement" {
		t.Errorf("replacement text = %q, want %q", text, "replacement")
	}
}

func TestDecideKeep(t *testing.T) {
	rs := NewReplacementState()
	// Empty replacement means "keep full content".
	rs.Decide("tu_1", "")

	_, replaced := rs.IsReplaced("tu_1")
	if replaced {
		t.Error("tu_1 with empty replacement should not be marked as replaced")
	}

	if !rs.IsSeen("tu_1") {
		t.Error("tu_1 should be seen")
	}
}

func TestDecideKeepIsFrozen(t *testing.T) {
	rs := NewReplacementState()
	// First: decide to keep.
	rs.Decide("tu_1", "")

	// Second: try to replace — should be rejected (frozen).
	ok := rs.Decide("tu_1", "new replacement")
	if ok {
		t.Error("second Decide should be rejected (frozen as keep)")
	}

	// Should still be "not replaced".
	_, replaced := rs.IsReplaced("tu_1")
	if replaced {
		t.Error("tu_1 should remain as keep after frozen")
	}
}

func TestIsSeenUnseen(t *testing.T) {
	rs := NewReplacementState()
	if rs.IsSeen("tu_unknown") {
		t.Error("unseen tool_use_id should return false")
	}
}

func TestIsReplacedUnseen(t *testing.T) {
	rs := NewReplacementState()
	_, ok := rs.IsReplaced("tu_unknown")
	if ok {
		t.Error("unseen tool_use_id should not be marked as replaced")
	}
}

func TestCounts(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "ref1")   // replaced
	rs.Decide("tu_2", "")       // kept
	rs.Decide("tu_3", "ref3")   // replaced

	if rs.SeenCount() != 3 {
		t.Errorf("SeenCount() = %d, want 3", rs.SeenCount())
	}
	if rs.ReplacedCount() != 2 {
		t.Errorf("ReplacedCount() = %d, want 2", rs.ReplacedCount())
	}
}

func TestMultipleIDs(t *testing.T) {
	rs := NewReplacementState()
	rs.Decide("tu_1", "ref1")
	rs.Decide("tu_2", "ref2")
	rs.Decide("tu_3", "")

	text1, ok1 := rs.IsReplaced("tu_1")
	text2, ok2 := rs.IsReplaced("tu_2")
	_, ok3 := rs.IsReplaced("tu_3")

	if !ok1 || text1 != "ref1" {
		t.Errorf("tu_1: replaced=%v, text=%q", ok1, text1)
	}
	if !ok2 || text2 != "ref2" {
		t.Errorf("tu_2: replaced=%v, text=%q", ok2, text2)
	}
	if ok3 {
		t.Error("tu_3 should not be replaced")
	}
}

func TestConcurrentDecide(t *testing.T) {
	rs := NewReplacementState()
	var wg sync.WaitGroup

	// Many goroutines try to decide the same ID — only one should win.
	wins := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok := rs.Decide("tu_race", "ref")
			wins <- ok
		}()
	}

	wg.Wait()
	close(wins)

	winCount := 0
	for ok := range wins {
		if ok {
			winCount++
		}
	}

	if winCount != 1 {
		t.Errorf("exactly 1 goroutine should win the Decide race, got %d", winCount)
	}

	if rs.SeenCount() != 1 {
		t.Errorf("SeenCount() = %d, want 1", rs.SeenCount())
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	rs := NewReplacementState()
	var wg sync.WaitGroup

	// Concurrent writes for different IDs.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "tu_" + string(rune('A'+n%26))
			rs.Decide(id, "ref")
		}(i)
	}

	// Concurrent reads.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.IsSeen("tu_A")
			rs.IsReplaced("tu_B")
			rs.SeenCount()
			rs.ReplacedCount()
		}()
	}

	wg.Wait()
	// No race detector failures = pass.
}
