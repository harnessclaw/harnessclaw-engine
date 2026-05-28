// Package spawn owns the full lifecycle of a sub-agent execution:
// session bootstrap, ToolPool filtering, prompt-profile resolution,
// permission inheritance, event forwarding, and final result assembly.
//
// SpawnSync (sync, blocking) and SpawnAsync (background) are the two
// public entry points. Both implement agent.AgentSpawner via the
// QueryEngine facade.
package spawn
