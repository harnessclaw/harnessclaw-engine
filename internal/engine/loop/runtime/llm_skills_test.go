package loopruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	skill "harnessclaw-go/internal/skills"

	"go.uber.org/zap"
)

func TestHydrateSkills_ModelInvocationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string
		wantDeny bool
	}{
		{name: "default"},
		{name: "explicitly allowed", flag: "disable-model-invocation: false\n"},
		{name: "manual only", flag: "disable-model-invocation: true\n", wantDeny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"normal", "candidate"} {
				dir := filepath.Join(root, name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				flag := ""
				if name == "candidate" {
					flag = tc.flag
				}
				content := "---\nname: " + name + "\n" + flag + "---\nInstructions for " + name + "."
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			reader := skill.NewReader([]string{root}, zap.NewNop())
			tr, block, err := hydrateSkills(reader, []string{"normal", "candidate"})
			if tc.wantDeny {
				if err == nil {
					t.Error("manual-only candidate should be denied")
				} else if !strings.Contains(err.Error(), "candidate") {
					t.Errorf("error does not identify candidate: %v", err)
				}
				if tr != nil || block != "" {
					t.Error("denied candidate produced a tracker or prompt block")
				}
				return
			}
			if err != nil {
				t.Fatalf("hydrateSkills: %v", err)
			}
			if tr.Count() != 2 || !tr.IsActive("normal") || !tr.IsActive("candidate") {
				t.Error("allowed candidates were not preloaded")
			}
			if !strings.Contains(block, "Instructions for normal.") || !strings.Contains(block, "Instructions for candidate.") {
				t.Error("allowed candidate instructions missing from prompt block")
			}
		})
	}
}
