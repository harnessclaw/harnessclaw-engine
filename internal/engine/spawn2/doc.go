// Package spawn2 is the new shape of spawn for the tier-decoupling
// refactor. During the transition (stages 3-7) it coexists with the
// legacy internal/engine/spawn package; Stage 8 deletes the legacy
// package and renames spawn2 → spawn.
//
// spawn2 is a TIER-AGNOSTIC primitive:
//   - Modules implement Module (declare SubagentType + Run)
//   - Spawner routes Sync calls by SubagentType
//   - Spawner.Async wraps Sync in a goroutine and returns a Handle
//
// spawn2 does NOT know about ExpectedOutputs, skill hydration, prompt
// construction, or scheduler strategies. Those live in tier modules
// under internal/engine/agent/.
//
// spawn2.Spawner satisfies agent.AgentSpawner so tools (agenttool,
// scheduler tool, freelance tool) can take *spawn2.Spawner without
// importing this package directly.
package spawn2
