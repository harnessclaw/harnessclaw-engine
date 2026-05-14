// Package failover implements a multi-provider LLM dispatcher that
// transparently fails over from a primary provider to ordered
// fallbacks when the primary returns classified failure-class errors,
// and probes the tripped provider after a cooldown so it can be
// promoted back to primary once it recovers.
package failover

import "time"

// cooldownPolicy configures the exponential-backoff schedule applied
// each time a provider is re-tripped without an intervening success.
//   - base   = first cooldown after the initial trip
//   - max    = upper bound (cap)
//   - factor = multiplier per consecutive trip (typically 2)
//
// Reset semantics: a successful call clears the consecutive-failure
// counter, so the next trip restarts at `base`.
type cooldownPolicy struct {
	base   time.Duration
	max    time.Duration
	factor int
}

func defaultCooldownPolicy() cooldownPolicy {
	return cooldownPolicy{
		base:   30 * time.Second,
		max:    10 * time.Minute,
		factor: 2,
	}
}

// providerState is the per-provider health record held by the failover
// dispatcher. The whole struct lives behind the dispatcher's mu — it
// is never accessed concurrently on its own.
type providerState struct {
	policy           cooldownPolicy
	consecutiveFails int           // number of trips since last recover()
	currentCooldown  time.Duration // cooldown applied by the most recent trip
	trippedUntil     time.Time     // zero value = never tripped
}

func newProviderState(p cooldownPolicy) *providerState {
	return &providerState{policy: p}
}

// healthy reports whether the provider has NEVER been tripped (or
// has been explicitly recovered). A "cooldown expired but not yet
// successfully probed" provider is NOT healthy — see shouldProbe.
// Strictly mutually exclusive with tripped / shouldProbe.
func (s *providerState) healthy(_ time.Time) bool {
	return s.trippedUntil.IsZero()
}

// tripped reports whether the provider is currently inside an active
// cooldown window.
func (s *providerState) tripped(now time.Time) bool {
	return !s.trippedUntil.IsZero() && now.Before(s.trippedUntil)
}

// shouldProbe reports whether the provider was tripped at some point
// AND its cooldown has now expired — i.e. the dispatcher is allowed
// to use this provider again as a probe. A successful probe call
// recover()s the state, returning the provider to Healthy.
func (s *providerState) shouldProbe(now time.Time) bool {
	return !s.trippedUntil.IsZero() && !now.Before(s.trippedUntil)
}

// trip marks the provider unavailable starting at `now`, using an
// exponentially-growing cooldown capped at policy.max. Consecutive
// trips without an intervening recover() compound the cooldown.
func (s *providerState) trip(now time.Time) {
	s.consecutiveFails++
	cooldown := s.policy.base
	factor := s.policy.factor
	if factor < 1 {
		factor = 1
	}
	for i := 1; i < s.consecutiveFails; i++ {
		next := cooldown * time.Duration(factor)
		if next > s.policy.max || next < cooldown { // overflow guard
			cooldown = s.policy.max
			break
		}
		cooldown = next
	}
	if s.policy.max > 0 && cooldown > s.policy.max {
		cooldown = s.policy.max
	}
	s.currentCooldown = cooldown
	s.trippedUntil = now.Add(cooldown)
}

// recover clears the consecutive-failure counter so the next trip
// starts from policy.base, and marks the provider Healthy by zeroing
// trippedUntil.
func (s *providerState) recover() {
	s.consecutiveFails = 0
	s.currentCooldown = 0
	s.trippedUntil = time.Time{}
}

// cooldown returns the duration applied by the most recent trip
// (zero before the first trip).
func (s *providerState) cooldown() time.Duration {
	return s.currentCooldown
}
