package principles

import (
	"strings"
	"testing"
)

// TestPrinciples_D19_NoBashMkdirDirective guards the prompt-side enforcement
// of D19 (no Bash mkdir/mv/cp for workspace management). The end-to-end
// runtime test that would inspect captured LLM events lives in cmd/ and is
// gitignored; this is the lightweight stand-in that fails fast if the
// directive is removed from the per-role principles files.
//
// We assert on Worker AND scheduler because both layers can call Bash —
// L3 directly (Read/Edit/Write users), L2 via scheduler.
func TestPrinciples_D19_NoBashMkdirDirective(t *testing.T) {
	cases := []struct {
		name string
		role Role
	}{
		{"worker", RoleWorker},
		{"scheduler", RoleScheduler},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := Principles(c.role)
			mustContain := []string{"mkdir", "cp"}
			for _, want := range mustContain {
				if !strings.Contains(text, want) {
					t.Errorf("principles[%s] missing %q — D19 directive removed?", c.role, want)
				}
			}
			if !strings.Contains(text, "bash") {
				t.Errorf("principles[%s] missing 'Bash' — directive must name the tool", c.role)
			}
		})
	}
}
