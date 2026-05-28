package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harnessclaw-go/internal/skill"
)

// testBuildLoadedSkillsBlock mirrors the engine package's buildLoadedSkillsBlock
// well enough for these unit tests — they only care that the wrapper tags
// and skill body strings land in the output, not that the formatting is
// byte-identical to the engine implementation.
func testBuildLoadedSkillsBlock(fulls []*skill.SkillFull) string {
	if len(fulls) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<loaded-skills>\n")
	for _, f := range fulls {
		sb.WriteString(`<skill name="`)
		sb.WriteString(f.Name)
		sb.WriteString(`">`)
		sb.WriteString("\n")
		sb.WriteString(f.Body)
		sb.WriteString("\n</skill>\n")
	}
	sb.WriteString("</loaded-skills>")
	return sb.String()
}

func TestParseCandidateSkills_Strings(t *testing.T) {
	inputs := map[string]any{"candidate_skills": []any{"a", "b"}}
	got := parseCandidateSkills(inputs)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("parseCandidateSkills = %v, want [a b]", got)
	}
}

func TestParseCandidateSkills_Nil(t *testing.T) {
	got := parseCandidateSkills(nil)
	if len(got) != 0 {
		t.Errorf("nil inputs → %v, want empty", got)
	}
}

func TestParseCandidateSkills_WrongType(t *testing.T) {
	inputs := map[string]any{"candidate_skills": "not-an-array"}
	got := parseCandidateSkills(inputs)
	if len(got) != 0 {
		t.Errorf("wrong-type → %v, want empty", got)
	}
}

func TestHydrateFreelancer_NoCandidates(t *testing.T) {
	reader := skill.NewReader([]string{t.TempDir()}, nil)
	prompt := "do the task"
	tracker, newPrompt, err := hydrateFreelancer(reader, testBuildLoadedSkillsBlock, nil, prompt)
	if err != nil {
		t.Fatalf("hydrateFreelancer: %v", err)
	}
	if tracker == nil {
		t.Fatal("tracker should be non-nil even with no candidates")
	}
	if newPrompt != prompt {
		t.Errorf("prompt changed without candidates: %q", newPrompt)
	}
}

func TestHydrateFreelancer_WithCandidates(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "echo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: echo\n---\nEcho body content"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := skill.NewReader([]string{tmp}, nil)
	prompt := "task body"
	tracker, newPrompt, err := hydrateFreelancer(reader, testBuildLoadedSkillsBlock, []string{"echo"}, prompt)
	if err != nil {
		t.Fatalf("hydrateFreelancer: %v", err)
	}
	if tracker.Count() != 1 {
		t.Errorf("tracker Count = %d, want 1", tracker.Count())
	}
	if !strings.Contains(newPrompt, "<loaded-skills>") {
		t.Errorf("prompt missing wrapper: %s", newPrompt)
	}
	if !strings.Contains(newPrompt, "Echo body content") {
		t.Errorf("prompt missing skill body: %s", newPrompt)
	}
	if !strings.HasSuffix(newPrompt, "task body") {
		t.Errorf("prompt should end with original task: %s", newPrompt)
	}
}

func TestHydrateFreelancer_MissingCandidate(t *testing.T) {
	reader := skill.NewReader([]string{t.TempDir()}, nil)
	_, _, err := hydrateFreelancer(reader, testBuildLoadedSkillsBlock, []string{"ghost"}, "task")
	if err == nil {
		t.Fatal("missing candidate should error")
	}
}

func TestHydrateFreelancer_TooManyCandidates(t *testing.T) {
	reader := skill.NewReader([]string{t.TempDir()}, nil)
	_, _, err := hydrateFreelancer(reader, testBuildLoadedSkillsBlock, []string{"a", "b", "c", "d"}, "task")
	if err == nil {
		t.Fatal("4 candidates should fail (max 3)")
	}
}
