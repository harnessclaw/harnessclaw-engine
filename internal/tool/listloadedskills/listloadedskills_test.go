package listloadedskills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
)

func TestListLoadedSkills_Empty(t *testing.T) {
	tr := loop.NewSkillTracker(3)
	ctx := tool.WithSkillTrackerValue(context.Background(), tr)
	tl := New(zap.NewNop())
	res, _ := tl.Execute(ctx, json.RawMessage(`{}`))
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"used":0`) {
		t.Errorf("budget used missing: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"max":3`) {
		t.Errorf("budget max missing: %s", res.Content)
	}
}

func TestListLoadedSkills_ActiveAndUnloaded(t *testing.T) {
	tr := loop.NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{{SkillCard: skill.SkillCard{Name: "cand", Version: "1"}, Body: "b"}})
	_ = tr.Add(&skill.SkillFull{SkillCard: skill.SkillCard{Name: "rt", Version: "2"}, Body: "b"})
	_ = tr.MarkUnloaded("cand")
	ctx := tool.WithSkillTrackerValue(context.Background(), tr)
	tl := New(zap.NewNop())
	res, _ := tl.Execute(ctx, json.RawMessage(`{}`))
	if !strings.Contains(res.Content, "rt") {
		t.Errorf("active list missing rt: %s", res.Content)
	}
	if !strings.Contains(res.Content, "cand") {
		t.Errorf("unloaded list missing cand: %s", res.Content)
	}
	if !strings.Contains(res.Content, `"used":1`) {
		t.Errorf("budget used should be 1: %s", res.Content)
	}
}

func TestListLoadedSkills_NoTracker(t *testing.T) {
	tl := New(zap.NewNop())
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.IsError {
		t.Fatal("no tracker should error")
	}
}
