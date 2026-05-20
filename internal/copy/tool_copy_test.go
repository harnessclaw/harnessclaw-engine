package copy

import (
	"strings"
	"testing"

	emitv2 "harnessclaw-go/internal/emit/v2"
)

func TestLookup_Hits(t *testing.T) {
	tmpls := lookup(CategoryWrite, emitv2.PhasePlanning)
	if len(tmpls) == 0 {
		t.Fatal("expected templates for (write, planning), got 0")
	}
	found := false
	for _, s := range tmpls {
		if strings.Contains(s, "构思") || strings.Contains(s, "草拟") {
			found = true
		}
	}
	if !found {
		t.Errorf("templates don't include expected 构思/草拟: %v", tmpls)
	}
}

func TestLookup_FallbackToGeneric(t *testing.T) {
	tmpls := lookup(CategoryRead, emitv2.PhasePermissionWait)
	if len(tmpls) == 0 {
		t.Fatal("expected fallback to generic templates, got 0")
	}
}

func TestLookup_TotalMiss(t *testing.T) {
	tmpls := lookup(CategoryGeneric, emitv2.ToolPhase("nonexistent_phase"))
	if len(tmpls) != 0 {
		t.Errorf("expected empty for unknown phase, got %v", tmpls)
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0B"},
		{234, "234B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{2 * 1024 * 1024, "1.0MB+"},
		{99 * 1024 * 1024, "1.0MB+"},
	}
	for _, c := range cases {
		if got := humanizeBytes(c.in); got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInterpolate(t *testing.T) {
	got := interpolate("已写入 {bytes}", 1024, nil)
	if !strings.Contains(got, "1.0KB") {
		t.Errorf("expected 1.0KB in %q", got)
	}

	got = interpolate("第 {attempt}/{max} 次", 0, &RetryInfo{Attempt: 2, Max: 5})
	if got != "第 2/5 次" {
		t.Errorf("got %q, want '第 2/5 次'", got)
	}

	got = interpolate("{seconds}s 后重试", 0, &RetryInfo{DelaySeconds: 3})
	if got != "3s 后重试" {
		t.Errorf("got %q, want '3s 后重试'", got)
	}
}
