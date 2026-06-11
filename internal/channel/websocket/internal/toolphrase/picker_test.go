package toolphrase

import (
	"math/rand"
	"testing"

	emitv2 "harnessclaw-go/internal/channel/emit/v2"
)

// fixedRng returns a Picker whose RNG is seeded so output is deterministic.
func fixedRng(seed int64) func() *rand.Rand {
	return func() *rand.Rand {
		return rand.New(rand.NewSource(seed))
	}
}

func TestPicker_DeterministicWithSeed(t *testing.T) {
	p := NewPicker(fixedRng(42))
	got1 := p.Pick("sess1", "write", emitv2.PhasePlanning, 0, nil)
	if got1 == "" {
		t.Fatal("expected non-empty pick")
	}
	got2 := p.Pick("sess1", "write", emitv2.PhasePlanning, 0, nil)
	if got2 == got1 {
		t.Errorf("rotation broken: same pick twice in same session: %q", got1)
	}
}

func TestPicker_RotationCoversAllCandidates(t *testing.T) {
	p := NewPicker(fixedRng(7))
	// PhasePlanning Write 类有 4 个候选
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		s := p.Pick("sess_rot", "write", emitv2.PhasePlanning, 0, nil)
		if seen[s] {
			t.Errorf("rotation broken at i=%d: repeated %q before exhausting set", i, s)
		}
		seen[s] = true
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 unique picks, got %d: %v", len(seen), seen)
	}
}

func TestPicker_RotationResetsAfterExhaustion(t *testing.T) {
	p := NewPicker(fixedRng(7))
	for i := 0; i < 4; i++ {
		p.Pick("sess_reset", "write", emitv2.PhasePlanning, 0, nil)
	}
	got := p.Pick("sess_reset", "write", emitv2.PhasePlanning, 0, nil)
	if got == "" {
		t.Error("expected reset to allow further picks, got empty")
	}
}

func TestPicker_SessionsIndependent(t *testing.T) {
	p := NewPicker(fixedRng(7))
	pickA := p.Pick("sessA", "write", emitv2.PhasePlanning, 0, nil)
	pickB := p.Pick("sessB", "write", emitv2.PhasePlanning, 0, nil)
	seen := map[string]bool{pickB: true}
	for i := 0; i < 3; i++ {
		s := p.Pick("sessB", "write", emitv2.PhasePlanning, 0, nil)
		if seen[s] {
			t.Errorf("sessB rotation polluted by sessA history: %q (pickA=%q)", s, pickA)
		}
		seen[s] = true
	}
}

func TestPicker_BytesInterpolated(t *testing.T) {
	p := NewPicker(fixedRng(7))
	s := p.Pick("sessX", "write", emitv2.PhasePlanningArgs, 1024, nil)
	if s == "" {
		t.Fatal("expected non-empty")
	}
	if !contains(s, "1.0KB") {
		t.Errorf("expected '1.0KB' in pick, got %q", s)
	}
}

func TestPicker_FallbackChain(t *testing.T) {
	p := NewPicker(fixedRng(7))
	s := p.Pick("sessFB", "read", emitv2.PhasePermissionWait, 0, nil)
	if s == "" {
		t.Error("expected fallback to generic, got empty")
	}
}

func TestPicker_UnknownToolFallsBackToGeneric(t *testing.T) {
	p := NewPicker(fixedRng(7))
	s := p.Pick("sessU", "DefinitelyNotARealTool", emitv2.PhasePlanning, 0, nil)
	if s == "" {
		t.Error("expected generic fallback for unknown tool")
	}
}

func TestPicker_Forget(t *testing.T) {
	p := NewPicker(fixedRng(7))
	p.Pick("sessForget", "bash", emitv2.PhasePlanning, 0, nil)
	p.Forget("sessForget")
	if p.activeSessionCount() != 0 {
		t.Errorf("after Forget, expected 0 sessions, got %d", p.activeSessionCount())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
