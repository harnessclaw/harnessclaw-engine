package tracker

import (
	"testing"

	"harnessclaw-go/internal/skills"
)

func mkFull(name, version string) *skill.SkillFull {
	return &skill.SkillFull{
		SkillCard: skill.SkillCard{Name: name, Version: version},
		Body:      "body of " + name,
	}
}

func TestSkillTracker_PreloadAndCount(t *testing.T) {
	tr := NewSkillTracker(3)
	if err := tr.Preload([]*skill.SkillFull{mkFull("a", "1"), mkFull("b", "1")}); err != nil {
		t.Fatalf("Preload: %v", err)
	}
	if tr.Count() != 2 {
		t.Errorf("Count = %d, want 2", tr.Count())
	}
}

func TestSkillTracker_Preload_ExceedsBudget(t *testing.T) {
	tr := NewSkillTracker(3)
	err := tr.Preload([]*skill.SkillFull{mkFull("a", "1"), mkFull("b", "1"), mkFull("c", "1"), mkFull("d", "1")})
	if err == nil {
		t.Fatal("Preload of 4 skills with budget 3 should fail")
	}
}

func TestSkillTracker_Add_BudgetEnforced(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{mkFull("a", "1"), mkFull("b", "1"), mkFull("c", "1")})
	err := tr.Add(mkFull("d", "1"))
	if err == nil {
		t.Fatal("Add at budget=3 should fail")
	}
}

func TestSkillTracker_MarkUnloaded_FreesBudget(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{mkFull("a", "1"), mkFull("b", "1"), mkFull("c", "1")})
	if err := tr.MarkUnloaded("a"); err != nil {
		t.Fatalf("MarkUnloaded: %v", err)
	}
	if tr.Count() != 2 {
		t.Errorf("Count after unload = %d, want 2", tr.Count())
	}
	if err := tr.Add(mkFull("d", "1")); err != nil {
		t.Errorf("Add after unload should succeed: %v", err)
	}
	if tr.Count() != 3 {
		t.Errorf("Count after re-add = %d, want 3", tr.Count())
	}
}

func TestSkillTracker_MarkUnloaded_Missing(t *testing.T) {
	tr := NewSkillTracker(3)
	if err := tr.MarkUnloaded("ghost"); err == nil {
		t.Fatal("MarkUnloaded of missing skill should error")
	}
}

func TestSkillTracker_Reactivate(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{mkFull("a", "1")})
	_ = tr.MarkUnloaded("a")
	if tr.Count() != 0 {
		t.Fatalf("after unload Count = %d", tr.Count())
	}
	if err := tr.Reactivate("a"); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if tr.Count() != 1 {
		t.Errorf("after reactivate Count = %d", tr.Count())
	}
}

func TestSkillTracker_Reactivate_AtBudget(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{mkFull("a", "1"), mkFull("b", "1"), mkFull("c", "1")})
	_ = tr.MarkUnloaded("a")    // active=2, unloaded=1
	_ = tr.Add(mkFull("d", "1")) // active=3
	if err := tr.Reactivate("a"); err == nil {
		t.Fatal("Reactivate when full should error")
	}
}

func TestSkillTracker_Add_DuplicateIsIdempotent(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Add(mkFull("a", "1"))
	if err := tr.Add(mkFull("a", "1")); err != nil {
		t.Errorf("duplicate Add of active skill: %v", err)
	}
	if tr.Count() != 1 {
		t.Errorf("duplicate Add changed Count: %d", tr.Count())
	}
}

func TestSkillTracker_List(t *testing.T) {
	tr := NewSkillTracker(3)
	_ = tr.Preload([]*skill.SkillFull{mkFull("cand", "1")})
	_ = tr.Add(mkFull("rt", "1"))
	_ = tr.MarkUnloaded("cand")
	active, unloaded := tr.List()
	if len(active) != 1 || active[0].Name != "rt" {
		t.Errorf("active = %+v", active)
	}
	if len(unloaded) != 1 || unloaded[0].Name != "cand" {
		t.Errorf("unloaded = %+v", unloaded)
	}
	if active[0].Source != "runtime" {
		t.Errorf("rt source = %q, want runtime", active[0].Source)
	}
	if unloaded[0].Source != "candidate" {
		t.Errorf("cand source = %q, want candidate", unloaded[0].Source)
	}
}

func TestSkillTracker_GetFull(t *testing.T) {
	tr := NewSkillTracker(3)
	full := mkFull("a", "1")
	_ = tr.Preload([]*skill.SkillFull{full})
	got, ok := tr.GetFull("a")
	if !ok || got.Name != "a" {
		t.Errorf("GetFull = %+v, ok=%v", got, ok)
	}
}
