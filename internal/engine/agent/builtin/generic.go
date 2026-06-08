package builtin

import (
	"harnessclaw-go/internal/legacy/agent"
)

// Generic is the fallback AgentDefinition used when a SpawnConfig's
// SubagentType has no specialised entry in the registry. It mirrors
// the behaviour of the legacy internal/engine/agent/generic module:
//
//   - WorkerProfile system prompt (Profile == "" resolves to worker)
//   - no AllowedTools whitelist (only the AgentType blacklist applies)
//   - 10-turn default cap when the spawn doesn't override
//   - StopOnEndTurn terminal hook (no SubmitTaskResult contract)
//
// The "generic" Name doubles as the AgentScope label so logs and
// EngineEvents read the same as before the migration.
var Generic = agent.AgentDefinition{
	Name:        "generic",
	DisplayName: "Generic Worker",
	Description: "Fallback worker for custom or unregistered subagent types.",
	// Tier left empty (= TierCoordinator) because the legacy generic
	// module was registered as a non-strict fallback — adopting
	// TierSubAgent here would force an OutputSchema requirement that
	// generic spawns historically don't carry.
	Profile:  "", // empty → prompt.WorkerProfile (see runner.resolveProfile)
	MaxTurns:    10,
	IsBuiltin:   true,
}
