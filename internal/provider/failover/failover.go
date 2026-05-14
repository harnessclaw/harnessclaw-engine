package failover

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// ErrAllProvidersDown is returned by Chat when every provider in the
// chain has been attempted in a single call and all returned
// failover-worthy errors. The most recent provider's error is wrapped
// inside so callers retain the diagnostic detail.
var ErrAllProvidersDown = errors.New("failover: all providers exhausted")

// Config configures a Failover dispatcher.
type Config struct {
	// Providers is the ordered chain. Index 0 is primary; subsequent
	// entries are tried in order when earlier ones are tripped.
	// Must be non-empty.
	Providers []provider.Provider

	// Names is parallel to Providers; entry i holds the chain entry
	// name (= llm.providers map key) so logs and Snapshot can
	// identify providers by their configured name instead of the
	// underlying adapter's generic Name(). When nil or empty,
	// names default to "chain[i]".
	Names []string

	// CooldownBase / CooldownMax / CooldownFactor configure the
	// per-provider exponential-backoff cooldown schedule. Zero
	// values use built-in defaults (30s / 10m / 2).
	CooldownBase   time.Duration
	CooldownMax    time.Duration
	CooldownFactor int

	// FastPolicy / MediumPolicy / ProbePolicy override the package
	// defaults (FastPolicy / MediumPolicy / ProbePolicy variables).
	// Zero-value RetryPolicy entries fall back to the defaults.
	FastPolicy   RetryPolicy
	MediumPolicy RetryPolicy
	ProbePolicy  RetryPolicy

	// Logger receives info/warn logs for failover and recover events.
	// Nil → no-op logger.
	Logger *zap.Logger
}

// Failover is a provider.Provider that fans out across a priority-
// ordered chain of inner providers with per-provider health tracking
// and policy-driven per-call budgets.
type Failover struct {
	providers    []provider.Provider
	names        []string
	logger       *zap.Logger
	fastPolicy   RetryPolicy
	mediumPolicy RetryPolicy
	probePolicy  RetryPolicy

	mu    sync.Mutex
	state []*providerState

	// now is the clock seam — overridable by tests.
	now func() time.Time

	// pickHook lets tests observe each pick() decision. Production
	// code leaves it nil. Called under f.mu; must not block or call
	// back into Failover.
	pickHook func(idx int, name string, policy RetryPolicy, probing bool)
}

// New constructs a Failover dispatcher.
func New(cfg Config) (*Failover, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("failover: at least one provider required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	cooldown := defaultCooldownPolicy()
	if cfg.CooldownBase > 0 {
		cooldown.base = cfg.CooldownBase
	}
	if cfg.CooldownMax > 0 {
		cooldown.max = cfg.CooldownMax
	}
	if cfg.CooldownFactor > 0 {
		cooldown.factor = cfg.CooldownFactor
	}
	fast := cfg.FastPolicy
	if fast == (RetryPolicy{}) {
		fast = FastPolicy
	}
	medium := cfg.MediumPolicy
	if medium == (RetryPolicy{}) {
		medium = MediumPolicy
	}
	probe := cfg.ProbePolicy
	if probe == (RetryPolicy{}) {
		probe = ProbePolicy
	}
	state := make([]*providerState, len(cfg.Providers))
	for i := range state {
		state[i] = newProviderState(cooldown)
	}
	names := make([]string, len(cfg.Providers))
	for i := range names {
		if i < len(cfg.Names) && cfg.Names[i] != "" {
			names[i] = cfg.Names[i]
		} else {
			names[i] = fmt.Sprintf("chain[%d]", i)
		}
	}
	return &Failover{
		providers:    cfg.Providers,
		names:        names,
		logger:       logger,
		fastPolicy:   fast,
		mediumPolicy: medium,
		probePolicy:  probe,
		state:        state,
		now:          time.Now,
	}, nil
}

// Name returns "failover(<n>)" so engine-level "provider initialized"
// log lines identify this layer specifically.
func (f *Failover) Name() string {
	return fmt.Sprintf("failover(%d)", len(f.providers))
}

// CountTokens delegates to the first (primary) provider. Token
// estimates are heuristic anyway; routing through the actual call
// chain would complicate the abstraction without measurable benefit.
func (f *Failover) CountTokens(ctx context.Context, msgs []types.Message) (int, error) {
	return f.providers[0].CountTokens(ctx, msgs)
}

// Chat dispatches the request across the chain. Routing rules:
//
//   1. pick() returns the highest-priority eligible provider plus
//      the policy that governs its budget.
//   2. We arm a budget tracker on a derived context; the timer
//      cancels the call if the provider doesn't return in time.
//   3. On synchronous error: disarm + classify; failover-worthy
//      errors trip the provider and the loop advances to the next
//      chain entry. Non-failover-worthy errors bubble up.
//   4. On successful sync return: disarm + wrap the stream to tap
//      the terminal Err() so the state machine learns of mid-stream
//      failures on next call.
//
// Returns ErrAllProvidersDown when every chain entry has been tried
// and all failed with failover-worthy errors.
func (f *Failover) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	tried := make(map[int]bool, len(f.providers))
	var lastErr error

	for attempt := 0; attempt < len(f.providers); attempt++ {
		idx, prov, policy, probing := f.pick(tried)
		if prov == nil {
			break
		}
		tried[idx] = true

		name := f.names[idx]
		if f.pickHook != nil {
			f.pickHook(idx, name, policy, probing)
		}

		attemptCtx, bt := armBudget(ctx, policy.Budget)

		stream, err := prov.Chat(attemptCtx, req)
		if err == nil {
			// Successful sync return — disarm budget so a later
			// timer fire doesn't kill an in-flight healthy stream.
			bt.disarm()
			captured := idx
			wrapped := wrapStream(stream,
				func() { f.recordSuccess(captured) },
				func(e error) { f.recordFailure(captured, e) },
			)
			if probing {
				f.logger.Info("failover: probing provider serving call",
					zap.Int("index", idx),
					zap.String("name", name),
					zap.String("policy", policy.Name),
				)
			}
			return wrapped, nil
		}

		// Synchronous error path.
		bt.disarm()

		if !FailoverWorthy(err) {
			// Caller / payload problem — bubble up. Do NOT trip the
			// provider (the failure isn't its fault).
			return nil, err
		}

		lastErr = err
		f.recordFailure(idx, err)
		f.logger.Warn("failover: provider tripped, trying next",
			zap.Int("index", idx),
			zap.String("name", name),
			zap.String("policy", policy.Name),
			zap.Error(err),
		)
	}

	if lastErr == nil {
		return nil, ErrAllProvidersDown
	}
	return nil, fmt.Errorf("%w: last error: %w", ErrAllProvidersDown, lastErr)
}

// pick implements the priority-respecting routing algorithm.
//
// The chain is walked in CONFIG ORDER (chain[0] = primary, chain[1] =
// first fallback, ...). The first provider that is eligible to serve
// the call wins:
//
//   - Healthy (never tripped / recovered):
//       remaining Healthy count after this pick > 0 → FastPolicy
//                                                    (bias toward speed
//                                                     because there's a
//                                                     fallback behind it)
//       remaining Healthy count == 0 (this is the LAST Healthy)
//                                                  → MediumPolicy
//                                                    (give it patience —
//                                                     no fallback to
//                                                     switch to)
//   - shouldProbe (tripped, cooldown expired):
//                                                  → ProbePolicy
//                                                    (cheap eligibility
//                                                     check; success
//                                                     recovers the
//                                                     provider)
//   - tripped (still cooling): skip
//
// Walking in chain order ensures recovery prefers the user's intended
// priority — a primary whose cooldown has lapsed is probed BEFORE a
// healthy fallback is reused, so the system gravitates back to the
// preferred provider.
//
// When no provider in the chain is Healthy or shouldProbe (every
// remaining entry is still in cooldown), the algorithm falls back to
// the LAST RESORT (Tier 3): pick the tripped provider with the
// EARLIEST trippedUntil — the one most likely to recover soonest —
// and hard-try with MediumPolicy (D1).
//
// Returns (-1, nil, RetryPolicy{}, false) when every chain entry has
// already been tried in this Chat() call.
func (f *Failover) pick(tried map[int]bool) (int, provider.Provider, RetryPolicy, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()

	// Count remaining Healthy providers (needed for the Fast vs Medium
	// decision when we pick a Healthy entry below).
	healthyTotal := 0
	for i, s := range f.state {
		if tried[i] {
			continue
		}
		if s.healthy(now) {
			healthyTotal++
		}
	}

	// Walk chain in priority order; first eligible entry wins.
	for i, s := range f.state {
		if tried[i] {
			continue
		}
		if s.healthy(now) {
			policy := f.fastPolicy
			if healthyTotal == 1 {
				policy = f.mediumPolicy
			}
			return i, f.providers[i], policy, false
		}
		if s.shouldProbe(now) {
			return i, f.providers[i], f.probePolicy, true
		}
		// tripped (still in cooldown) — skip this entry, keep looking
		// down the chain.
	}

	// Tier 3: every remaining entry is in active cooldown. Pick the
	// one whose cooldown expires soonest and hard-try it.
	bestIdx := -1
	var bestUntil time.Time
	for i, s := range f.state {
		if tried[i] {
			continue
		}
		if bestIdx < 0 || s.trippedUntil.Before(bestUntil) {
			bestIdx = i
			bestUntil = s.trippedUntil
		}
	}
	if bestIdx >= 0 {
		return bestIdx, f.providers[bestIdx], f.mediumPolicy, false
	}

	return -1, nil, RetryPolicy{}, false
}

// recordSuccess marks the provider Healthy and resets its
// consecutive-failure counter. Logged at INFO when the provider had
// been tripped (recovery event); otherwise silent (normal success
// doesn't need a log line).
func (f *Failover) recordSuccess(idx int) {
	f.mu.Lock()
	wasTripped := !f.state[idx].trippedUntil.IsZero()
	f.state[idx].recover()
	f.mu.Unlock()
	if wasTripped {
		f.logger.Info("failover: provider recovered",
			zap.Int("index", idx),
			zap.String("name", f.names[idx]),
		)
	}
}

// recordFailure trips the provider with the next exponential cooldown.
func (f *Failover) recordFailure(idx int, _ error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[idx].trip(f.now())
}

// state is a test-only accessor — assertions on cooldown progression
// shouldn't have to round-trip through Chat() events.
func (f *Failover) state_(idx int) *providerState { //nolint:unused
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state[idx]
}

// ProviderHealth is a per-provider snapshot exposed to the
// management API. The shape is a stable JSON contract for the
// console client.
type ProviderHealth struct {
	// Index in the chain (0 = primary).
	Index int `json:"index"`
	// Name is the provider name as configured in llm.providers.
	Name string `json:"name"`
	// State is one of "healthy" / "tripped" / "ready_to_probe".
	// Strictly mutually exclusive — see providerState semantics.
	State string `json:"state"`
	// TrippedUntil is RFC3339 timestamp when cooldown expires
	// (empty when never tripped).
	TrippedUntil string `json:"tripped_until,omitempty"`
	// CooldownSeconds is the most recent cooldown duration applied
	// (0 when never tripped).
	CooldownSeconds int `json:"cooldown_seconds"`
	// ConsecutiveFailures since the last recover().
	ConsecutiveFailures int `json:"consecutive_failures"`
}

// Snapshot returns a point-in-time view of every provider's health
// state. Safe to call concurrently with Chat — runs under f.mu.
func (f *Failover) Snapshot() []ProviderHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	out := make([]ProviderHealth, len(f.state))
	for i, s := range f.state {
		state := "healthy"
		var trippedUntil string
		switch {
		case s.tripped(now):
			state = "tripped"
			trippedUntil = s.trippedUntil.UTC().Format(time.RFC3339)
		case s.shouldProbe(now):
			state = "ready_to_probe"
			trippedUntil = s.trippedUntil.UTC().Format(time.RFC3339)
		}
		out[i] = ProviderHealth{
			Index:               i,
			Name:                f.names[i],
			State:               state,
			TrippedUntil:        trippedUntil,
			CooldownSeconds:     int(s.currentCooldown / time.Second),
			ConsecutiveFailures: s.consecutiveFails,
		}
	}
	return out
}

// ----- budget tracker ---------------------------------------------

// budgetTracker arms an idle-cancel timer on a derived context.
// disarm() stops the timer AND prevents future cancel firing, both
// under mutex so there's no race window between Chat() returning
// successfully and the timer firing on the stream's ctx.
type budgetTracker struct {
	mu        sync.Mutex
	timer     *time.Timer
	cancel    context.CancelCauseFunc
	triggered bool
}

// armBudget returns a context derived from ctx that is cancelled if
// the budget elapses, plus a tracker whose disarm() must be called
// once the provider's sync return happens (regardless of outcome).
// budget <= 0 means "no cap" — returns ctx unchanged and a nil
// tracker.
func armBudget(ctx context.Context, budget time.Duration) (context.Context, *budgetTracker) {
	if budget <= 0 {
		return ctx, nil
	}
	derivedCtx, cancel := context.WithCancelCause(ctx)
	bt := &budgetTracker{cancel: cancel}
	bt.timer = time.AfterFunc(budget, func() {
		bt.mu.Lock()
		defer bt.mu.Unlock()
		if bt.triggered {
			return
		}
		bt.triggered = true
		cancel(fmt.Errorf("failover: provider budget %s exceeded", budget))
	})
	return derivedCtx, bt
}

// disarm stops the timer and flags the tracker so any in-flight
// timer callback waiting on the mutex becomes a no-op.
func (bt *budgetTracker) disarm() {
	if bt == nil {
		return
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	bt.triggered = true
	bt.timer.Stop()
}
