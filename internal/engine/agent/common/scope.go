package common

import (
	"harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
)

// BuildAgentScope assembles the per-spawn tool.AgentScope from the
// SpawnConfig and the engine-wide root dir. Tools like meta_write and
// submit_task_result read SessionRoot/TaskID/Agent from this scope via
// tool.AgentScopeFromCtx; without it they fail with "SessionRoot missing
// in ctx — engine configuration error".
//
// rootDir empty (e.g. no $HOME on a containerised build) yields
// SessionRoot=="", which scope-aware tools treat as "no scope" and
// reject. Callers should still pass it through — the resulting empty
// scope is preferable to leaving ctx unscoped.
//
// fallbackAgent is used when cfg.SubagentType is empty (legacy callers
// that didn't set it). Modules pass their own tier name as the fallback
// so meta_write's Agent field still attributes the work meaningfully.
func BuildAgentScope(cfg *SpawnConfig, rootDir, fallbackAgent string) tool.AgentScope {
	agentName := cfg.SubagentType
	if agentName == "" {
		agentName = fallbackAgent
	}
	var root string
	if rootDir != "" && cfg.RootSessionID != "" {
		root = workspace.SessionRoot(rootDir, cfg.RootSessionID)
	}
	return tool.AgentScope{
		SessionRoot: root,
		TaskID:      cfg.TaskID,
		Agent:       agentName,
	}
}
