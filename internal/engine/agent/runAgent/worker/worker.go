// Package worker is the L3 execution layer used by agentrun.ModeScheduled.
// It owns the ConsumerPool that dequeues L2→L3 task messages, runs them
// against the injected AgentSpawner, and publishes lifecycle + result
// messages back to L2 via msgbus.
//
// This package replaces the legacy internal/subagent package as part of
// the agentrun unification (P4). Communication with L2 is exclusively
// via msgbus; communication with the parent client stream goes through
// the per-task EventRegistry instead of the package-global SetOutCh of
// the legacy implementation.
package worker

import (
	"harnessclaw-go/internal/engine/agent/scheduler/spec"
)

// LeafSpec is an alias for spec.TaskSpec restricted to L3 execution params.
// (Aliased to avoid duplicate field maintenance; runtime treats it as a TaskSpec.)
type LeafSpec = spec.TaskSpec
