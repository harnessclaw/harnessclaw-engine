package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// EnsureSession is idempotent. First call creates session root, tasks/,
// deliverables/, and writes an empty Plan skeleton to plan.json. Subsequent
// calls leave existing content alone. Returns error only on filesystem
// failures, not on "already exists".
func EnsureSession(rootDir, sessionID string) error {
	for _, d := range []string{
		SessionRoot(rootDir, sessionID),
		TasksDir(rootDir, sessionID),
		DeliverablesDir(rootDir, sessionID),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("ensure %s: %w", d, err)
		}
	}
	planPath := PlanPath(rootDir, sessionID)
	if _, err := os.Stat(planPath); errors.Is(err, os.ErrNotExist) {
		skeleton := &Plan{
			SessionID: sessionID,
			CreatedAt: time.Now().UTC(),
			Tasks:     map[string]*Task{},
		}
		b, err := json.MarshalIndent(skeleton, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal skeleton: %w", err)
		}
		if err := writeFileAtomic(planPath, b, 0o644); err != nil {
			return fmt.Errorf("write skeleton: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat plan.json: %w", err)
	}
	return nil
}

// EnsureTaskDir is idempotent MkdirAll for one task dir.
func EnsureTaskDir(rootDir, sessionID, taskID string) error {
	return os.MkdirAll(TaskDir(rootDir, sessionID, taskID), 0o755)
}

// WipeTaskDir removes all contents under {taskDir} but keeps the dir itself.
// Used by retry: each retry starts from a clean slate. If the dir does not
// exist, it is created (so callers don't need a separate "does it exist?"
// check before calling).
func WipeTaskDir(rootDir, sessionID, taskID string) error {
	dir := TaskDir(rootDir, sessionID, taskID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.MkdirAll(dir, 0o755)
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return nil
}

// writeFileAtomic writes content via {path}.tmp + rename so concurrent
// readers never see a half-written file. Used by bootstrap and the
// plan_writer's consumer goroutine.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
