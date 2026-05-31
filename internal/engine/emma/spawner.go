package emma

import (
	"harnessclaw-go/internal/engine/spawn"
)

// Spawner exposes the underlying spawn.Spawner so callers can wire it
// into tools and infrastructure that need to dispatch sub-agents.
//
// emma.Engine no longer implements agent.AgentSpawner directly; tools
// and the engine's Coordinator/PlanExecutor/QueryEngineFactory take
// *spawn.Spawner via this accessor.
func (e *Engine) Spawner() *spawn.Spawner { return e.spawner }
