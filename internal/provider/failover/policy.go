package failover

import "time"

// RetryPolicy bundles the per-call wall-clock budget applied by the
// Failover dispatcher to a selected provider's Chat() call.
//
// The dispatcher does NOT implement its own retry loop — retry is
// owned by the engine layer's retry.Retryer wrapping the dispatcher
// from outside. Budget here exists only to bound how long we wait
// on a single provider's HTTP-level dial / handshake before treating
// the call as a failover-worthy failure and advancing to the next
// chain entry.
//
// Three named tiers cover all Failover-internal routing decisions
// (Probe / Fast / Medium). A fourth implicit tier "Full" applies to
// single-provider deployments, which bypass Failover entirely and
// inherit the engine retry layer's full budget directly.
type RetryPolicy struct {
	// Name identifies the policy for logs / metrics. Carried alongside
	// Budget so observability tools can group by routing decision
	// without scraping numeric thresholds.
	Name string

	// Budget caps the wall-clock duration the dispatcher will wait on
	// the provider before cancelling the attempt and routing to the
	// next chain entry. 0 means "no cap — let the underlying ctx
	// decide".
	//
	// The budget covers the WHOLE call lifecycle the dispatcher can
	// observe (sync Chat() return AND stream consumption, until
	// disarm() runs after successful sync return). The race window
	// where the timer fires nanoseconds before disarm is benign:
	// stream consumers see ctx.Cancelled, the engine retry treats it
	// as a transient error, and the dispatcher's state machine
	// trips the provider on the next call.
	Budget time.Duration
}

// Default policies, used when Config does not override them.
//
// The numbers are picked to balance "release the primary's resilience"
// against "user-visible latency cap":
//
//   - Probe (5s):   one-shot eligibility check after cooldown; if the
//                   provider can't even open a connection in 5s, it's
//                   still tripped — don't waste user time on it.
//   - Fast (15s):   primary with at least one Healthy fallback behind
//                   it; aggressive enough to mask brief outages,
//                   patient enough to absorb one round of normal
//                   reasoning + first-byte latency.
//   - Medium (30s): last-Healthy or last-resort hard-try; no
//                   fallback to switch to, so wait longer. Capped to
//                   keep total response time bounded.
var (
	ProbePolicy  = RetryPolicy{Name: "probe", Budget: 5 * time.Second}
	FastPolicy   = RetryPolicy{Name: "fast", Budget: 15 * time.Second}
	MediumPolicy = RetryPolicy{Name: "medium", Budget: 30 * time.Second}
)
