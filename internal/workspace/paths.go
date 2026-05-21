// Package workspace owns the on-disk layout shared across L1/L2/L3 agents.
// All paths derive from a single (rootDir, rootSessionID, taskID?) triple so
// callers never hand-assemble strings.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultRootDir returns the on-disk root for the local-files-as-truth
// layout. Defaults to ~/.harnessclaw/workspace, matching the convention
// used elsewhere (skills dir, session DB). Returns "" when UserHomeDir
// fails (containerised builds with no $HOME); callers degrade gracefully
// by treating that as "scope disabled".
func DefaultRootDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".harnessclaw", "workspace")
}

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

// mustSafe panics if id is empty or contains path-traversal sequences.
// IDs are engine-internal: a bad value is a programmer bug, not a runtime
// condition, so panic surfaces it immediately instead of silently sanitising.
func mustSafe(id, what string) {
	if id == "" {
		panic(fmt.Sprintf("workspace: empty %s", what))
	}
	if strings.ContainsAny(id, "/\\") || id == "." || strings.Contains(id, "..") {
		panic(fmt.Sprintf("workspace: unsafe %s %q", what, id))
	}
}
