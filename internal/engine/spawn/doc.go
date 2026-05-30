// Package spawn is the tier-decoupled spawn primitive. It was introduced
// as spawn2 during the Stage 3-7 refactor and renamed to spawn in Stage
// 8 after the legacy package was deleted.
//
// spawn is a TIER-AGNOSTIC primitive:
//   - Modules implement Module (declare SubagentType + Run)
//   - Spawner routes Sync calls by SubagentType
//   - Spawner.Async wraps Sync in a goroutine and returns a Handle
//
// spawn does NOT know about ExpectedOutputs, skill hydration, prompt
// construction, or scheduler strategies. Those live in tier modules
// under internal/engine/agent/.
//
// spawn.Spawner satisfies agent.AgentSpawner so tools (agenttool,
// scheduler tool, freelance tool) can take *spawn.Spawner without
// importing this package directly.
package spawn
