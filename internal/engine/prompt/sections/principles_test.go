package sections

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts"
)

func TestPrinciplesSection_MentionsSpecialistsDelegation(t *testing.T) {
	s := NewPrinciplesSection()
	out, err := s.Render(&prompt.PromptContext{}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// emma's full principles must point to Specialists as the single
	// delegation entry. Agent / Orchestrate must NOT appear — they are
	// L2-internal in the 3-tier architecture.
	for _, must := range []string{"Specialists", "task"} {
		if !strings.Contains(out, must) {
			t.Errorf("emma principles missing %q", must)
		}
	}
	for _, mustNotHave := range []string{"Agent", "Orchestrate"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("emma principles must not mention %q (L2-internal)", mustNotHave)
		}
	}
}

func TestPrinciplesSection_CompactMentionsSpecialists(t *testing.T) {
	out := texts.PrinciplesCompact(texts.RoleEmma)
	if !strings.Contains(out, "Specialists") {
		t.Errorf("compact principles missing Specialists:\n%s", out)
	}
	for _, mustNotHave := range []string{"Agent", "Orchestrate"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("compact principles must not mention %q (L2-internal)", mustNotHave)
		}
	}
}

func TestPrinciplesSection_MentionsSearchAndClarify(t *testing.T) {
	s := NewPrinciplesSection()
	out, err := s.Render(&prompt.PromptContext{}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// emma must reference her own L1 capabilities (search + clarify) and
	// the L2 abstraction she delegates to (Specialists / 专业团). She must
	// NOT reference specific L3 sub-agent codenames — that's L2's concern.
	for _, must := range []string{
		"WebSearch",
		"TavilySearch",
		"AskUserQuestion",
		"专业团",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("emma principles missing %q", must)
		}
	}
	for _, mustNotHave := range []string{"小瑞", "小林", "小数", "小程", "小悦", "小时"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("emma principles must not name L3 sub-agent codename %q (team is now black-boxed under Specialists)", mustNotHave)
		}
	}
}

func TestPrinciplesSection_CompactMentionsSearchAndClarify(t *testing.T) {
	out := texts.PrinciplesCompact(texts.RoleEmma)
	for _, must := range []string{"WebSearch", "AskUserQuestion", "Specialists"} {
		if !strings.Contains(out, must) {
			t.Errorf("compact principles missing %q:\n%s", must, out)
		}
	}
	for _, mustNotHave := range []string{"小瑞", "小林"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("compact principles must not name L3 sub-agent codename %q", mustNotHave)
		}
	}
}
