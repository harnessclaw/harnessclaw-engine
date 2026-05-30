package emma

import (
	"context"
	"errors"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/engine/spawn"
)

// --- agent.AgentSpawner facade. spawner does the real work.

// SpawnSync implements agent.AgentSpawner. Dispatches to spawner,
// which picks a tier module by SubagentType (plan_agent,
// plan_executor_agent, explore, plan, plan_design, freelancer,
// scheduler) and falls back to the generic module for unknown types.
func (e *Engine) SpawnSync(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	return e.spawner.Sync(ctx, cfg)
}

// SpawnAsync implements agent.AsyncSpawner. After Stage 8 the legacy
// async path (taskRegistry + WorkerNotification broker round-trip) is
// gone; the spawn.Handle-based replacement is not yet wired up. Tools
// that request RunInBackground get a clear error until the new path
// lands.
func (e *Engine) SpawnAsync(ctx context.Context, cfg *agent.SpawnConfig) (string, error) {
	return "", errors.New("SpawnAsync: not implemented in the new spawn architecture yet")
}

// Spawner exposes the underlying spawn.Spawner so tests in package
// emma can reach module-level helpers without exporting them by name.
func (e *Engine) Spawner() *spawn.Spawner { return e.spawner }
