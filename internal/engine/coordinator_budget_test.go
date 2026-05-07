package engine

import (
	"sync"
	"testing"
	"time"

	"harnessclaw-go/pkg/types"
)

func TestBudgetTracker_TokenLimit(t *testing.T) {
	tr := NewBudgetTracker(BudgetLimit{MaxTokens: 1000}).Start()

	tr.AddUsage(&types.Usage{InputTokens: 400, OutputTokens: 100})
	if exceeded, _ := tr.Exceeded(); exceeded {
		t.Errorf("budget should not be exceeded after 500 tokens of 1000")
	}

	tr.AddUsage(&types.Usage{InputTokens: 400, OutputTokens: 200})
	exceeded, why := tr.Exceeded()
	if !exceeded {
		t.Fatalf("budget should be exceeded after 1100 tokens of 1000")
	}
	if why == "" {
		t.Error("Exceeded() reason should be non-empty when tripped")
	}
}

func TestBudgetTracker_FailureLimit(t *testing.T) {
	tr := NewBudgetTracker(BudgetLimit{MaxFailures: 3}).Start()

	tr.NoteFailure()
	tr.NoteFailure()
	if ok, _ := tr.Exceeded(); ok {
		t.Errorf("2 failures of 3 should not exceed budget")
	}
	tr.NoteFailure()
	if ok, _ := tr.Exceeded(); !ok {
		t.Errorf("3 failures of 3 should exceed budget (>=, not >)")
	}
}

func TestBudgetTracker_LLMCallLimit(t *testing.T) {
	tr := NewBudgetTracker(BudgetLimit{MaxLLMCalls: 2}).Start()
	tr.AddUsage(nil)
	tr.AddUsage(nil)
	if ok, _ := tr.Exceeded(); !ok {
		t.Error("LLM call limit should trigger at >= MaxLLMCalls")
	}
}

func TestBudgetTracker_DurationLimit(t *testing.T) {
	tr := NewBudgetTracker(BudgetLimit{MaxDuration: 50 * time.Millisecond}).Start()
	if ok, _ := tr.Exceeded(); ok {
		t.Errorf("freshly started tracker should not be over duration")
	}
	time.Sleep(60 * time.Millisecond)
	if ok, _ := tr.Exceeded(); !ok {
		t.Error("tracker should report duration exceeded after sleeping past limit")
	}
}

func TestBudgetTracker_NoLimitsNeverExceeds(t *testing.T) {
	// Zero-valued BudgetLimit means "no limit" — the test asserts that
	// behaviour explicitly so it can't regress silently.
	tr := NewBudgetTracker(BudgetLimit{}).Start()
	tr.AddUsage(&types.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	tr.NoteFailure()
	tr.NoteFailure()
	if ok, _ := tr.Exceeded(); ok {
		t.Errorf("zero-limit tracker must never report exceeded")
	}
}

func TestBudgetTracker_ConcurrentAddUsage(t *testing.T) {
	// BudgetTracker is mutated from multiple sub-agent goroutines in
	// parallel-mode plan execution. A torn-write here means budget gets
	// silently undercounted — the failure mode would be "task ran 2x
	// over budget but Exceeded() never fired".
	tr := NewBudgetTracker(BudgetLimit{}).Start()
	var wg sync.WaitGroup
	const N = 100
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.AddUsage(&types.Usage{InputTokens: 1, OutputTokens: 1})
		}()
	}
	wg.Wait()
	snap := tr.Snapshot()
	if snap.TokensUsed != 2*N {
		t.Errorf("concurrent AddUsage lost updates: got %d, want %d", snap.TokensUsed, 2*N)
	}
	if snap.LLMCalls != N {
		t.Errorf("concurrent AddUsage lost call count: got %d, want %d", snap.LLMCalls, N)
	}
}

func TestDefaultPlanBudget_SaneValues(t *testing.T) {
	b := DefaultPlanBudget()
	if b.MaxTokens <= 0 || b.MaxDuration <= 0 || b.MaxFailures <= 0 || b.MaxLLMCalls <= 0 {
		t.Errorf("DefaultPlanBudget should have non-zero limits, got %+v", b)
	}
}
