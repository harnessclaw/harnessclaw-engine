package unloadskill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
)

func mkCtx(tr *engine.SkillTracker) context.Context {
	return tool.WithSkillTrackerValue(context.Background(), tr)
}

func mkFull(name string) *skill.SkillFull {
	return &skill.SkillFull{SkillCard: skill.SkillCard{Name: name}, Body: "body"}
}

func TestUnloadSkill_Success(t *testing.T) {
	tr := engine.NewSkillTracker(3)
	_ = tr.Add(mkFull("a"))
	tl := New(zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "a"})
	res, err := tl.Execute(mkCtx(tr), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if tr.Count() != 0 {
		t.Errorf("after unload Count = %d", tr.Count())
	}
	if len(res.NewMessages) != 1 {
		t.Fatalf("expected 1 NewMessage")
	}
	text := ""
	for _, b := range res.NewMessages[0].Content {
		text += b.Text
	}
	if !strings.Contains(text, "<skill-unloaded") || !strings.Contains(text, `name="a"`) {
		t.Errorf("notice format wrong: %s", text)
	}
}

func TestUnloadSkill_NotLoaded(t *testing.T) {
	tr := engine.NewSkillTracker(3)
	tl := New(zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "ghost"})
	res, _ := tl.Execute(mkCtx(tr), raw)
	if !res.IsError {
		t.Error("unloading missing skill should error")
	}
}

func TestUnloadSkill_AlreadyUnloaded(t *testing.T) {
	tr := engine.NewSkillTracker(3)
	_ = tr.Add(mkFull("a"))
	_ = tr.MarkUnloaded("a")
	tl := New(zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "a"})
	res, _ := tl.Execute(mkCtx(tr), raw)
	if !res.IsError {
		t.Error("double unload should error")
	}
}

func TestUnloadSkill_NoTracker(t *testing.T) {
	tl := New(zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"skill": "a"})
	res, _ := tl.Execute(context.Background(), raw)
	if !res.IsError {
		t.Error("no tracker should error")
	}
}
