package spawn

import (
	"context"

	"harnessclaw-go/internal/agent"
)

// Module is what each tier module implements. The module decides:
//   - which SubagentType string it answers to
//   - what its Run does (assemble inputs, call loop.Run, build result)
//
// Modules are constructed at startup and registered with Spawner.
type Module interface {
	// SubagentType returns the SubagentType string this module handles.
	// Used by Spawner.Register to derive the registry key (eliminates
	// hand-aligned magic strings).
	SubagentType() string

	// Run executes the sub-agent synchronously. Async behavior is
	// handled by Spawner wrapping Run in a goroutine; modules
	// themselves are always synchronous.
	Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error)
}
