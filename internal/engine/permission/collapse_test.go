package permission

import (
	"context"
	"testing"
)

// In default mode the permission prompts are collapsed: only bash + browser
// still Ask; every other write tool auto-allows. (File ops outside the
// workspace are gated separately by AgentScope escalation, not here.)
func TestDefaultModeCollapsedPrompts(t *testing.T) {
	t.Parallel()
	c := NewOuterChecker(ModeDefault, nil)
	cases := []struct {
		tool       string
		readOnly   bool
		wantAsk    bool
	}{
		{"bash", false, true},
		{"browser_agent", false, true},
		{"agent_browser_command", false, true},
		{"browser_session_create", false, true},
		{"edit", false, false},          // file write → allow (scope handles out-of-workspace)
		{"write", false, false},
		{"image_generate", false, false},
		{"video_create", false, false},
		{"plan_update", false, false},
		{"meta_write", false, false},
		{"scheduler", false, false},
		{"read", true, false},           // read-only → allow
		{"web_search", true, false},
	}
	for _, c2 := range cases {
		res := c.Check(context.Background(), c2.tool, nil, c2.readOnly)
		gotAsk := res.Decision == Ask
		if gotAsk != c2.wantAsk {
			t.Errorf("tool %q: decision=%s, wantAsk=%v", c2.tool, res.Decision, c2.wantAsk)
		}
	}
}
