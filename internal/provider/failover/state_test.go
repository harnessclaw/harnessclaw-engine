package failover

import (
	"testing"
	"time"
)

func TestProviderState_NewStartsHealthy(t *testing.T) {
	s := newProviderState(defaultCooldownPolicy())
	if !s.healthy(time.Now()) {
		t.Fatalf("new state should be healthy")
	}
	if s.tripped(time.Now()) {
		t.Fatalf("new state should not be tripped")
	}
}

func TestProviderState_TripUsesBaseCooldownThenExponential(t *testing.T) {
	now := time.Unix(0, 0)
	s := newProviderState(cooldownPolicy{base: 30 * time.Second, max: 10 * time.Minute, factor: 2})

	s.trip(now)
	if got, want := s.cooldown(), 30*time.Second; got != want {
		t.Fatalf("first trip cooldown = %v, want %v", got, want)
	}
	if !s.tripped(now.Add(15 * time.Second)) {
		t.Fatalf("expected tripped within first cooldown")
	}
	if s.tripped(now.Add(45 * time.Second)) {
		t.Fatalf("expected NOT tripped after cooldown expires")
	}

	// Second trip without an intervening success doubles the cooldown.
	s.trip(now.Add(45 * time.Second))
	if got, want := s.cooldown(), 60*time.Second; got != want {
		t.Fatalf("second trip cooldown = %v, want %v", got, want)
	}
}

func TestProviderState_RecoverResetsFailureBudget(t *testing.T) {
	now := time.Unix(0, 0)
	s := newProviderState(cooldownPolicy{base: 30 * time.Second, max: 10 * time.Minute, factor: 2})
	s.trip(now)
	s.trip(now.Add(45 * time.Second)) // bumps to 60s
	s.recover()
	s.trip(now.Add(2 * time.Minute))
	if got, want := s.cooldown(), 30*time.Second; got != want {
		t.Fatalf("post-recover trip cooldown = %v, want %v (should reset to base)", got, want)
	}
}

func TestProviderState_ProbeOnExpiredCooldown(t *testing.T) {
	now := time.Unix(0, 0)
	s := newProviderState(cooldownPolicy{base: 30 * time.Second, max: 10 * time.Minute, factor: 2})
	s.trip(now)
	// During cooldown — not eligible for probe.
	if s.shouldProbe(now.Add(10 * time.Second)) {
		t.Fatalf("should not probe during cooldown")
	}
	// After cooldown — eligible.
	if !s.shouldProbe(now.Add(31 * time.Second)) {
		t.Fatalf("should probe after cooldown expires")
	}
}

func TestProviderState_MaxCooldownCap(t *testing.T) {
	now := time.Unix(0, 0)
	s := newProviderState(cooldownPolicy{base: 30 * time.Second, max: 2 * time.Minute, factor: 2})
	for i := 0; i < 10; i++ {
		s.trip(now)
		now = now.Add(s.cooldown())
	}
	if got, want := s.cooldown(), 2*time.Minute; got != want {
		t.Fatalf("cooldown should be capped at max=%v, got %v", want, got)
	}
}
