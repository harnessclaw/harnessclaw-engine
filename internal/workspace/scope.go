package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// L3SpawnScope computes per-spawn workspace paths (this task's dir,
// upstream input paths, ReadScope, WriteScope) for one L3 dispatch.
//
// Inputs:
//   - rootDir, rootSID: the workspace root and root-session anchor.
//   - taskID: this spawn's identifier; becomes the on-disk task dir name.
//     Must be safe (workspace.mustSafe rules) — callers validate at the
//     planning layer.
//   - deps: upstream task IDs already completed; their meta.json is read to
//     expand each into its declared output paths.
//
// Returns ("", nil, nil, nil) when rootDir, rootSID, or taskID is empty.
// Callers in that case fall back to SpawnConfig's "no restriction" branch
// (legacy compat for containerised builds with no $HOME).
func L3SpawnScope(rootDir, rootSID, taskID string, deps []string) (taskDir string, inputPaths []string, readScope []string, writeScope []string) {
	if rootDir == "" || rootSID == "" || taskID == "" {
		return "", nil, nil, nil
	}
	taskDir = TaskDir(rootDir, rootSID, taskID)
	readScope = append(readScope, taskDir)
	writeScope = []string{taskDir}

	for _, dep := range deps {
		if dep == "" {
			continue
		}
		depDir := TaskDir(rootDir, rootSID, dep)
		readScope = append(readScope, depDir)
		// Expand the dep's declared outputs into input paths so the L3
		// preamble shows actual data files with their summaries. Missing
		// or malformed meta.json is silently skipped — the L3 still has
		// read access to the dep's dir and can FileRead anything inside.
		metaB, err := os.ReadFile(MetaPath(rootDir, rootSID, dep))
		if err != nil {
			continue
		}
		var m Meta
		if err := json.Unmarshal(metaB, &m); err != nil {
			continue
		}
		for _, o := range m.Outputs {
			if o.Path != "" {
				inputPaths = append(inputPaths, o.Path)
			}
		}
	}
	return taskDir, inputPaths, readScope, writeScope
}

// ReconcileSpawnReturn is the L2-side post-spawn audit (D14 fallback):
// after an L3 finishes, read meta.json and update the plan.json task
// entry accordingly. Returns true when the L3 produced a valid meta.json
// (status=done in plan); false when meta is missing/invalid (status=failed
// in plan, caller decides whether to wipe + retry).
//
// All mutations flow through the supplied PlanWriter so the state machine
// stays single-consumer per session. Empty rootDir/rootSID/taskID
// short-circuits to false — callers in legacy / containerised paths skip
// the update entirely.
func ReconcileSpawnReturn(ctx context.Context, writer *PlanWriter, rootDir, rootSID, taskID string) (bool, error) {
	if writer == nil || rootDir == "" || rootSID == "" || taskID == "" {
		return false, nil
	}
	metaRel := "tasks/" + taskID + "/meta.json"
	metaAbs := MetaPath(rootDir, rootSID, taskID)

	b, err := os.ReadFile(metaAbs)
	if err != nil {
		// Missing meta.json — D8 兜底: mark plan task failed. Caller
		// inspects the returned bool to decide whether to call
		// wipe_for_retry + re-spawn.
		applyErr := writer.Apply(ctx, func(p *Plan) error {
			task, ok := p.Tasks[taskID]
			if !ok {
				// Plan never knew about this task (legacy dispatcher);
				// nothing to reconcile, treat as not-our-business.
				return nil
			}
			if task.Frozen {
				return nil
			}
			task.Status = StatusFailed
			task.EndedAt = time.Now().UTC()
			return nil
		})
		if applyErr != nil {
			return false, fmt.Errorf("plan reconcile (missing meta): %w", applyErr)
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read meta: %w", err)
	}

	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		applyErr := writer.Apply(ctx, func(p *Plan) error {
			task, ok := p.Tasks[taskID]
			if !ok || task.Frozen {
				return nil
			}
			task.Status = StatusFailed
			task.EndedAt = time.Now().UTC()
			return nil
		})
		if applyErr != nil {
			return false, fmt.Errorf("plan reconcile (malformed meta): %w", applyErr)
		}
		return false, fmt.Errorf("parse meta: %w", err)
	}
	if validateErr := m.Validate(); validateErr != nil {
		applyErr := writer.Apply(ctx, func(p *Plan) error {
			task, ok := p.Tasks[taskID]
			if !ok || task.Frozen {
				return nil
			}
			task.Status = StatusFailed
			task.EndedAt = time.Now().UTC()
			return nil
		})
		if applyErr != nil {
			return false, fmt.Errorf("plan reconcile (invalid meta): %w", applyErr)
		}
		return false, fmt.Errorf("meta validation: %w", validateErr)
	}

	applyErr := writer.Apply(ctx, func(p *Plan) error {
		task, ok := p.Tasks[taskID]
		if !ok {
			return nil
		}
		if task.Frozen {
			return nil
		}
		task.Status = StatusDone
		task.SummaryRef = metaRel
		task.EndedAt = time.Now().UTC()
		return nil
	})
	if applyErr != nil {
		return false, fmt.Errorf("plan reconcile (done): %w", applyErr)
	}
	return true, nil
}
