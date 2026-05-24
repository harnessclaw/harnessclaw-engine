// Package subagent is the L3 execution layer. It exposes only the consumer pool +
// runner; communication with L2 is exclusively via msgbus.
package subagent

import (
	"harnessclaw-go/internal/engine/scheduler/spec"
)

// LeafSpec is an alias for spec.TaskSpec restricted to L3 execution params.
// (Aliased to avoid duplicate field maintenance; runtime treats it as a TaskSpec.)
type LeafSpec = spec.TaskSpec
