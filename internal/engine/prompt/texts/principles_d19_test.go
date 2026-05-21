package texts

import (
	"strings"
	"testing"
)

// TestPrinciples_D19_NoBashMkdirDirective guards the prompt-side enforcement
// of D19 (no Bash mkdir/mv/cp for workspace management). The end-to-end
// runtime test that would inspect captured LLM events lives in cmd/ and is
// gitignored; this is the lightweight stand-in that fails fast if the
// directive is removed from principles.go.
//
// We assert on Worker AND Specialists because both layers can call Bash —
// L3 directly (Read/Edit/Write users), L2 via specialists.
func TestPrinciples_D19_NoBashMkdirDirective(t *testing.T) {
	cases := []struct {
		name string
		role Role
	}{
		{"worker", RoleWorker},
		{"specialists", RoleSpecialists},
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
			if !strings.Contains(text, "Bash") {
				t.Errorf("principles[%s] missing 'Bash' — directive must name the tool", c.role)
			}
		})
	}
}
