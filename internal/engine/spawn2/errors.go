package spawn2

import "errors"

// ErrUnknownSubagentType is returned by Spawner.Sync when the
// SubagentType has no registered Module AND no fallback was set
// via SetFallback. Forces typos to surface early instead of being
// silently routed to a generic catch-all.
var ErrUnknownSubagentType = errors.New("spawn2: unknown SubagentType, no module registered and no fallback set")

// ErrDuplicateRegistration is returned (as panic) when Register is
// called twice with the same SubagentType.
var ErrDuplicateRegistration = errors.New("spawn2: duplicate registration for SubagentType")
