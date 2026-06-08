package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"harnessclaw-go/internal/legacy/workspace"
)

// WriteMeta serialises m to {targetDir}/meta.json and returns the relative path
// (relative to sessionRoot if known via root, else basename "meta.json").
func WriteMeta(_ context.Context, targetDir, sessionRoot string, m workspace.Meta) (string, error) {
	if m.EndedAt.IsZero() {
		m.EndedAt = time.Now().UTC()
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	abs := filepath.Join(targetDir, "meta.json")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(sessionRoot, abs)
	if err != nil {
		rel = "meta.json"
	}
	return rel, nil
}
