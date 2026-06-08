package workspace

import (
	"strings"
	"testing"
)

func TestRenderTaskInputs_EmptyReturnsEmpty(t *testing.T) {
	if got := RenderTaskInputs(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := RenderTaskInputs([]TaskInputRef{}); got != "" {
		t.Errorf("zero-length slice got %q, want empty", got)
	}
}

func TestRenderTaskInputs_ContainsPathsAndSummaries(t *testing.T) {
	inputs := []TaskInputRef{
		{Path: "tasks/t_001/output.md", Summary: "5家竞品对比", Bytes: 8423},
		{Path: "tasks/t_002/data.csv", Summary: "原始销量表", Bytes: 4096},
	}
	got := RenderTaskInputs(inputs)
	for _, want := range []string{
		"<task-inputs>", "</task-inputs>",
		"tasks/t_001/output.md", "5家竞品对比",
		"tasks/t_002/data.csv", "原始销量表",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestRenderTaskInputs_BytesRendered(t *testing.T) {
	got := RenderTaskInputs([]TaskInputRef{
		{Path: "x.bin", Bytes: 2048},
	})
	if !strings.Contains(got, "2.0KB") {
		t.Errorf("expected size 2.0KB in output, got: %s", got)
	}
}

func TestRenderTaskInputs_EmptySummaryOmitted(t *testing.T) {
	got := RenderTaskInputs([]TaskInputRef{
		{Path: "x.md", Bytes: 100},
	})
	if strings.Contains(got, " — ") {
		t.Errorf("dash separator should be absent when summary empty: %s", got)
	}
	if !strings.Contains(got, "x.md") {
		t.Errorf("path missing: %s", got)
	}
}

func TestWrapTaskWithInputs_EmptyInputsNoChange(t *testing.T) {
	if got := WrapTaskWithInputs("hello", nil); got != "hello" {
		t.Errorf("unexpected change: %q", got)
	}
}

func TestWrapTaskWithInputs_BothPresent(t *testing.T) {
	got := WrapTaskWithInputs("写报告", []TaskInputRef{{Path: "tasks/t_001/output.md", Summary: "调研"}})
	if !strings.Contains(got, "<task>") || !strings.Contains(got, "</task>") {
		t.Errorf("missing <task> wrapper tags: %q", got)
	}
	if !strings.Contains(got, "<task-inputs>") {
		t.Errorf("missing preamble: %q", got)
	}
	// Order matters: inputs preamble must come BEFORE <task>
	if strings.Index(got, "<task-inputs>") >= strings.Index(got, "<task>") {
		t.Errorf("preamble must come before task block")
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{2 * 1024 * 1024, "2.0MB"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
