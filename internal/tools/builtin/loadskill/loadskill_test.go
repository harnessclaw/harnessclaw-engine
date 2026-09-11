package loadskill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skill "harnessclaw-go/internal/skills"
	"harnessclaw-go/internal/skills/tracker"
	tool "harnessclaw-go/internal/tools"

	"go.uber.org/zap"
)

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\nversion: \"1\"\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkCtx(tr *tracker.SkillTracker) context.Context {
	return tool.WithSkillTrackerValue(context.Background(), tr)
}

func TestLoadSkill_NoTracker(t *testing.T) {
	tmp := t.TempDir()
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "anything"})
	res, _ := tl.Execute(context.Background(), raw)
	if !res.IsError {
		t.Fatal("expected error when tracker absent")
	}
}

func TestLoadSkill_Success(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "echo", "Echo skill body")
	tracker := tracker.NewSkillTracker(3)
	reader := skill.NewReader([]string{tmp}, zap.NewNop())
	tl := New(reader, zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "echo"})
	res, err := tl.Execute(mkCtx(tracker), raw)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if tracker.Count() != 1 {
		t.Errorf("tracker Count = %d, want 1", tracker.Count())
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("expected 1 NewMessage, got %d", len(res.NewMessages))
	}
	msgText := ""
	for _, b := range res.NewMessages[0].Content {
		msgText += b.Text
	}
	if !strings.Contains(msgText, `<skill name="echo"`) {
		t.Errorf("NewMessage missing <skill> tag: %s", msgText)
	}
	if !strings.Contains(msgText, `root="`) {
		t.Errorf("NewMessage missing root attr: %s", msgText)
	}
	if !strings.Contains(msgText, "Echo skill body") {
		t.Errorf("NewMessage missing body: %s", msgText)
	}
}

func TestLoadSkill_Idempotent_Active(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "echo", "body")
	tracker := tracker.NewSkillTracker(3)
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "echo"})
	_, _ = tl.Execute(mkCtx(tracker), raw)
	res, _ := tl.Execute(mkCtx(tracker), raw)
	if res.IsError {
		t.Errorf("second Load should be idempotent: %s", res.Content)
	}
	if tracker.Count() != 1 {
		t.Errorf("Count after idempotent reload = %d", tracker.Count())
	}
}

func TestLoadSkill_BudgetFull_NewSkill(t *testing.T) {
	tmp := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		writeSkill(t, tmp, n, "body "+n)
	}
	tracker := tracker.NewSkillTracker(3)
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	for _, n := range []string{"a", "b", "c"} {
		raw, _ := json.Marshal(map[string]any{"skill": n})
		_, _ = tl.Execute(mkCtx(tracker), raw)
	}
	raw, _ := json.Marshal(map[string]any{"skill": "d"})
	res, _ := tl.Execute(mkCtx(tracker), raw)
	if !res.IsError {
		t.Fatalf("d at budget=3/3 should fail")
	}
	if !strings.Contains(res.Content, "budget") && !strings.Contains(res.Content, "full") {
		t.Errorf("error message should mention budget: %s", res.Content)
	}
}

func TestLoadSkill_Reactivate_Unloaded(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "echo", "body")
	tracker := tracker.NewSkillTracker(3)
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "echo"})
	_, _ = tl.Execute(mkCtx(tracker), raw)
	_ = tracker.MarkUnloaded("echo")
	if tracker.Count() != 0 {
		t.Fatalf("after MarkUnloaded Count = %d", tracker.Count())
	}
	res, _ := tl.Execute(mkCtx(tracker), raw)
	if res.IsError {
		t.Errorf("reactivate failed: %s", res.Content)
	}
	if tracker.Count() != 1 {
		t.Errorf("after reactivate Count = %d", tracker.Count())
	}
}

func TestLoadSkill_BodyTooLarge(t *testing.T) {
	tmp := t.TempDir()
	big := strings.Repeat("x", 105*1024) // >100KB
	writeSkill(t, tmp, "huge", big)
	tracker := tracker.NewSkillTracker(3)
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "huge"})
	res, _ := tl.Execute(mkCtx(tracker), raw)
	if !res.IsError {
		t.Fatal("body >100KB should be rejected")
	}
}

func TestLoadSkill_NotFound(t *testing.T) {
	tmp := t.TempDir()
	tracker := tracker.NewSkillTracker(3)
	tl := New(skill.NewReader([]string{tmp}, zap.NewNop()), zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "ghost"})
	res, _ := tl.Execute(mkCtx(tracker), raw)
	if !res.IsError {
		t.Fatal("missing skill should error")
	}
}

func TestLoadSkill_DisableModelInvocation(t *testing.T) {
	for _, preloaded := range []bool{false, true} {
		name := "new"
		if preloaded {
			name = "unloaded"
		}
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			writeSkill(t, tmp, "manual-only", "")
			content := "---\nname: manual-only\ndisable-model-invocation: true\n---\nManual-only instructions."
			if err := os.WriteFile(filepath.Join(tmp, "manual-only", "SKILL.md"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			reader := skill.NewReader([]string{tmp}, zap.NewNop())
			tr := tracker.NewSkillTracker(3)
			if preloaded {
				full, err := reader.Load("manual-only")
				if err != nil {
					t.Fatalf("explicit Load: %v", err)
				}
				if err := tr.Preload([]*skill.SkillFull{full}); err != nil {
					t.Fatal(err)
				}
				if err := tr.MarkUnloaded("manual-only"); err != nil {
					t.Fatal(err)
				}
			}

			tl := New(reader, zap.NewNop())
			res, err := tl.Execute(mkCtx(tr), json.RawMessage(`{"skill":"manual-only"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !res.IsError {
				t.Errorf("model load should be denied, got %q", res.Content)
			}
			if len(res.NewMessages) != 0 || strings.Contains(res.Content, "Manual-only instructions.") {
				t.Error("denied model load exposed skill instructions")
			}
			if tr.Count() != 0 || tr.IsActive("manual-only") || tr.IsTracked("manual-only") != preloaded {
				t.Error("denied model load changed tracker state")
			}
		})
	}
}
