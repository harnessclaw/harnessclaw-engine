package prompt

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/skill"
)

func TestBuildLoadedSkillsBlock_Empty(t *testing.T) {
	got := BuildLoadedSkillsBlock(nil)
	if got != "" {
		t.Errorf("empty input → %q, want empty", got)
	}
}

func TestBuildLoadedSkillsBlock_WrapsAllSkills(t *testing.T) {
	fulls := []*skill.SkillFull{
		{SkillCard: skill.SkillCard{Name: "a", Version: "1", Path: "/skills/a"}, Body: "body A"},
		{SkillCard: skill.SkillCard{Name: "b", Path: "/skills/b"}, Body: "body B"},
	}
	got := BuildLoadedSkillsBlock(fulls)
	if !strings.Contains(got, "<loaded-skills>") || !strings.Contains(got, "</loaded-skills>") {
		t.Errorf("missing wrapper tag: %s", got)
	}
	if !strings.Contains(got, `<skill name="a" version="1" root="/skills/a">`) {
		t.Errorf("missing skill a attrs: %s", got)
	}
	if !strings.Contains(got, "body A") || !strings.Contains(got, "body B") {
		t.Errorf("missing bodies: %s", got)
	}
}

func TestBuildSingleSkillBlock_Attrs(t *testing.T) {
	full := &skill.SkillFull{
		SkillCard: skill.SkillCard{Name: "x", Version: "2", Path: "/skills/x"},
		Body:      "Hi",
	}
	got := BuildSingleSkillBlock(full)
	want := `<skill name="x" version="2" root="/skills/x">` + "\nHi\n</skill>"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
