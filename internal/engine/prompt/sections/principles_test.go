package sections

import (
	"strings"
	"testing"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/prompt/texts/principles"
)

func TestPrinciplesSection_MentionsSchedulerDelegation(t *testing.T) {
	s := NewPrinciplesSection()
	out, err := s.Render(&prompt.PromptContext{}, 1<<20)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// emma's full principles must point to scheduler as the single
	// delegation entry. orchestrate / specialists must NOT appear — they
	// are legacy / renamed in the 3-tier architecture.
	for _, must := range []string{"scheduler", "task"} {
		if !strings.Contains(out, must) {
			t.Errorf("emma principles missing %q", must)
		}
	}
	for _, mustNotHave := range []string{"orchestrate", "specialists"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("emma principles must not mention %q (legacy / renamed)", mustNotHave)
		}
	}
}

func TestPrinciplesSection_CompactMentionsScheduler(t *testing.T) {
	out := principles.PrinciplesCompact(principles.RoleEmma)
	if !strings.Contains(out, "scheduler") {
		t.Errorf("compact principles missing scheduler:\n%s", out)
	}
	for _, mustNotHave := range []string{"orchestrate", "specialists"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("compact principles must not mention %q (legacy / renamed)", mustNotHave)
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
	// the L2 abstraction she delegates to (scheduler / 专业团). She must
	// NOT reference specific L3 sub-agent codenames — that's L2's concern.
	for _, must := range []string{
		"web_search",
		"tavily_search",
		"ask_user_question",
		"专业团",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("emma principles missing %q", must)
		}
	}
	for _, mustNotHave := range []string{"小瑞", "小林", "小数", "小程", "小悦", "小时"} {
		if strings.Contains(out, mustNotHave) {
			t.Errorf("emma principles must not name L3 sub-agent codename %q (team is now black-boxed under scheduler)", mustNotHave)
		}
	}
}

func TestPrinciplesSection_CompactMentionsSearchAndClarify(t *testing.T) {
	out := principles.PrinciplesCompact(principles.RoleEmma)
	for _, must := range []string{"web_search", "ask_user_question", "scheduler"} {
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
