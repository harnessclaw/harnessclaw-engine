// Package spawn owns the full lifecycle of a sub-agent execution:
// session bootstrap, ToolPool filtering, prompt-profile resolution,
// permission inheritance, event forwarding, and final result assembly.
//
// SpawnSync (sync, blocking) and SpawnAsync (background) are the two
// public entry points. emma.Engine satisfies agent.AgentSpawner by
// delegating to a spawn.Spawner instance constructed in emma.New —
// callers never touch spawn.Spawner directly.
package spawn
