package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPaths_AllUnderSessionRoot(t *testing.T) {
	root := "/tmp/hcw"
	sid := "sess_abc"
	got := map[string]string{
		"session": SessionRoot(root, sid),
		"tasks":   TasksDir(root, sid),
		"task":    TaskDir(root, sid, "t_001"),
		"deliv":   DeliverablesDir(root, sid),
		"plan":    PlanPath(root, sid),
		"meta":    MetaPath(root, sid, "t_001"),
	}
	want := SessionRoot(root, sid)
	for k, p := range got {
		if !strings.HasPrefix(p, want+string(filepath.Separator)) && p != want {
			t.Errorf("%s = %q, expected to be inside or equal to %q", k, p, want)
		}
	}
}

func TestPaths_TaskDirIsUnderTasks(t *testing.T) {
	root := "/tmp/hcw"
	tasks := TasksDir(root, "sess_a")
	task := TaskDir(root, "sess_a", "t_42")
	if filepath.Dir(task) != tasks {
		t.Errorf("TaskDir parent = %q, want %q", filepath.Dir(task), tasks)
	}
}

func TestPaths_NoTraversalInTaskID(t *testing.T) {
	root := "/tmp/hcw"
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic for traversal taskID")
		}
	}()
	_ = TaskDir(root, "sess_a", "../escape")
}

func TestPaths_NoTraversalInSessionID(t *testing.T) {
	root := "/tmp/hcw"
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic for traversal sessionID")
		}
	}()
	_ = SessionRoot(root, "../escape")
}
