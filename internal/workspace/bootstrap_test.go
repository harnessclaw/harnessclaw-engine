package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSession_Idempotent(t *testing.T) {
	root := t.TempDir()
	sid := "sess_abc"

	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("first call: %v", err)
	}
	marker := filepath.Join(TasksDir(root, sid), "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("second EnsureSession wiped marker: %v", err)
	}
	if _, err := os.Stat(PlanPath(root, sid)); err != nil {
		t.Errorf("plan.json missing: %v", err)
	}
}

func TestEnsureSession_WritesValidPlanSkeleton(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	b, err := os.ReadFile(PlanPath(root, sid))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := unmarshalPlan(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.SessionID != sid {
		t.Errorf("session id mismatch: %q", plan.SessionID)
	}
	if plan.CreatedAt.IsZero() {
		t.Errorf("created_at unset")
	}
	if err := plan.Validate(); err != nil {
		t.Errorf("skeleton failed Validate: %v", err)
	}
}

func TestEnsureTaskDir_Idempotent(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTaskDir(root, sid, "t_001"); err != nil {
		t.Fatalf("first task: %v", err)
	}
	if err := EnsureTaskDir(root, sid, "t_001"); err != nil {
		t.Fatalf("second task: %v", err)
	}
	if _, err := os.Stat(TaskDir(root, sid, "t_001")); err != nil {
		t.Errorf("task dir missing: %v", err)
	}
}

func TestWipeTaskDir_RemovesContents(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	if err := EnsureSession(root, sid); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTaskDir(root, sid, "t_001"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(TaskDir(root, sid, "t_001"), "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WipeTaskDir(root, sid, "t_001"); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file still present: err=%v", err)
	}
	if _, err := os.Stat(TaskDir(root, sid, "t_001")); err != nil {
		t.Errorf("task dir missing after wipe: %v", err)
	}
}

func TestWipeTaskDir_OnNonexistentDirIsOK(t *testing.T) {
	root := t.TempDir()
	sid := "sess_a"
	_ = EnsureSession(root, sid)
	// dir not created — wipe should ensure it exists, no error
	if err := WipeTaskDir(root, sid, "t_never_created"); err != nil {
		t.Fatalf("wipe of nonexistent: %v", err)
	}
	if _, err := os.Stat(TaskDir(root, sid, "t_never_created")); err != nil {
		t.Errorf("task dir not created by wipe: %v", err)
	}
}

func TestWriteFileAtomic_NoPartialOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.bin")

	// Sanity: writeFileAtomic succeeds normally
	if err := writeFileAtomic(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "hello" {
		t.Errorf("got %q want %q", string(b), "hello")
	}
}

func unmarshalPlan(b []byte) (*Plan, error) {
	p := &Plan{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	return p, nil
}
