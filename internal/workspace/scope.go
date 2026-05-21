package workspace

import (
	"encoding/json"
	"os"
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
