// Package workspace owns the on-disk layout shared across L1/L2/L3 agents.
// All paths derive from a single (rootDir, rootSessionID, taskID?) triple so
// callers never hand-assemble strings.
package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SessionRoot is {rootDir}/session/{sessionID}.
func SessionRoot(rootDir, sessionID string) string {
	mustSafe(sessionID, "sessionID")
	return filepath.Join(rootDir, "session", sessionID)
}

// TasksDir is {sessionRoot}/tasks.
func TasksDir(rootDir, sessionID string) string {
	return filepath.Join(SessionRoot(rootDir, sessionID), "tasks")
}

// TaskDir is {sessionRoot}/tasks/{taskID}.
func TaskDir(rootDir, sessionID, taskID string) string {
	mustSafe(taskID, "taskID")
	return filepath.Join(TasksDir(rootDir, sessionID), taskID)
}

// DeliverablesDir is {sessionRoot}/deliverables.
func DeliverablesDir(rootDir, sessionID string) string {
	return filepath.Join(SessionRoot(rootDir, sessionID), "deliverables")
}

// PlanPath is {sessionRoot}/plan.json.
func PlanPath(rootDir, sessionID string) string {
	return filepath.Join(SessionRoot(rootDir, sessionID), "plan.json")
}

// MetaPath is {taskDir}/meta.json.
func MetaPath(rootDir, sessionID, taskID string) string {
	return filepath.Join(TaskDir(rootDir, sessionID, taskID), "meta.json")
}

// mustSafe panics on identifiers containing path traversal / separators.
// We panic instead of returning error because these IDs originate inside
// the engine (not LLM-supplied) — a bad value here is a programmer bug
// that should surface loudly, not be silently sanitised.
func mustSafe(id, what string) {
	if id == "" {
		panic(fmt.Sprintf("workspace: empty %s", what))
	}
	if strings.ContainsAny(id, "/\\") || id == "." || id == ".." || strings.Contains(id, "..") {
		panic(fmt.Sprintf("workspace: unsafe %s %q", what, id))
	}
}
