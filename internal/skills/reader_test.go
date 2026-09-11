package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestReader_Search_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	r := NewReader([]string{tmp}, zap.NewNop())
	got, err := r.Search("", 20)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 skills, got %d", len(got))
	}
}

func writeSkill(t *testing.T, root, name, fmYaml, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + fmYaml + "\n---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReader_Search_ReturnsCardMetadata(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "notion-export",
		"name: notion-export\ndescription: export to notion\nwhen_to_use: when user asks notion\nversion: 0.3\nallowed-tools:\n  - web_fetch",
		"body content here")
	r := NewReader([]string{tmp}, zap.NewNop())
	got, err := r.Search("", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	card := got[0]
	if card.Name != "notion-export" {
		t.Errorf("Name = %q, want notion-export", card.Name)
	}
	if card.Description != "export to notion" {
		t.Errorf("Description = %q", card.Description)
	}
	if card.WhenToUse != "when user asks notion" {
		t.Errorf("WhenToUse = %q", card.WhenToUse)
	}
	if card.Version != "0.3" {
		t.Errorf("Version = %q", card.Version)
	}
	if len(card.AllowedTools) != 1 || card.AllowedTools[0] != "web_fetch" {
		t.Errorf("AllowedTools = %v", card.AllowedTools)
	}
	if card.Path == "" {
		t.Errorf("Path is empty — Load() needs it")
	}
}

func TestReader_Search_QueryFilter(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "notion-export",
		"name: notion-export\ndescription: send content to Notion",
		"body")
	writeSkill(t, tmp, "figma-to-code",
		"name: figma-to-code\ndescription: generate code from Figma frame",
		"body")
	r := NewReader([]string{tmp}, zap.NewNop())
	got, err := r.Search("notion", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].Name != "notion-export" {
		t.Fatalf("query=notion → %+v, want only notion-export", got)
	}
}

func TestReader_Load_ReturnsBody(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "echo",
		"name: echo\ndescription: echo back",
		"# Echo skill\nDo what user asks, echo back the result.")
	r := NewReader([]string{tmp}, zap.NewNop())
	full, err := r.Load("echo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(full.Body, "Echo skill") {
		t.Errorf("Body missing content: %q", full.Body)
	}
	if full.Name != "echo" {
		t.Errorf("Name = %q", full.Name)
	}
}

func TestReader_Load_NotFound(t *testing.T) {
	tmp := t.TempDir()
	r := NewReader([]string{tmp}, zap.NewNop())
	_, err := r.Load("does-not-exist")
	if err == nil {
		t.Fatal("Load(missing) returned nil error")
	}
}

func TestReader_BadFrontmatter_Skipped(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "good", "name: good\ndescription: ok", "body")
	// Malformed YAML — colon mid-value, unquoted
	writeSkill(t, tmp, "bad", "name: bad\ndescription: : : nope ::", "body")
	r := NewReader([]string{tmp}, zap.NewNop())
	got, _ := r.Search("", 20)
	// "bad" may parse OK depending on YAML laxness; assert at least "good" is here
	foundGood := false
	for _, c := range got {
		if c.Name == "good" {
			foundGood = true
		}
	}
	if !foundGood {
		t.Errorf("good skill missing despite bad sibling")
	}
}

func TestReader_Cache_TTL(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "v1", "name: v1\ndescription: first", "body")
	r := NewReader([]string{tmp}, zap.NewNop())
	first, _ := r.Search("", 20)
	if len(first) != 1 {
		t.Fatalf("first scan: %d cards", len(first))
	}
	// Add a new skill; within TTL it should NOT appear
	writeSkill(t, tmp, "v2", "name: v2\ndescription: second", "body")
	second, _ := r.Search("", 20)
	if len(second) != 1 {
		t.Errorf("within TTL second scan: %d cards, expected cached 1", len(second))
	}
}

func TestSkillCard_JSON_OmitsPath(t *testing.T) {
	c := SkillCard{Name: "x", Path: "/secret/path"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), `"path"`) {
		t.Errorf("SkillCard JSON leaks Path field: %s", string(b))
	}
	if strings.Contains(string(b), `/secret/path`) {
		t.Errorf("SkillCard JSON leaks Path value: %s", string(b))
	}
}

func TestReader_Search_ExcludesModelDisabledSkills(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "a-manual",
		"name: a-manual\ndescription: shared task\ndisable-model-invocation: true", "manual body")
	writeSkill(t, tmp, "b-enabled",
		"name: b-enabled\ndescription: shared task\ndisable-model-invocation: false", "enabled body")
	writeSkill(t, tmp, "c-default",
		"name: c-default\ndescription: shared task", "default body")
	r := NewReader([]string{tmp}, zap.NewNop())
	for _, tc := range []struct {
		query string
		limit int
		want  string
	}{
		{"", 20, "b-enabled,c-default"},
		{"a-manual", 20, ""},
		{"shared", 20, "b-enabled,c-default"},
		{"", 1, "b-enabled"},
		{"a-manual shared", 1, "b-enabled"},
	} {
		got, err := r.Search(tc.query, tc.limit)
		if err != nil {
			t.Fatalf("Search(%q, %d): %v", tc.query, tc.limit, err)
		}
		var names []string
		for _, card := range got {
			names = append(names, card.Name)
		}
		if joined := strings.Join(names, ","); joined != tc.want {
			t.Errorf("Search(%q, %d) = %q, want %q", tc.query, tc.limit, joined, tc.want)
		}
	}
	// Filtering the model's search results must not discard the cached entry
	// needed to explicitly load a manual-only skill.
	full, err := r.Load("a-manual")
	if err != nil {
		t.Fatalf("Load after Search: %v", err)
	}
	if full.Body != "manual body" {
		t.Errorf("explicit Load body = %q, want manual body", full.Body)
	}
}

func TestReader_ModelInvocationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     string
		disabled bool
	}{
		{"manual-only", "\ndisable-model-invocation: true", true},
		{"enabled", "\ndisable-model-invocation: false", false},
		{"default", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			writeSkill(t, tmp, "style", "name: style\ndescription: output style"+tc.flag, "Action first.")
			r := NewReader([]string{tmp}, zap.NewNop())
			full, err := r.Load("style")
			if err != nil {
				t.Fatalf("explicit Load: %v", err)
			}
			if full.Body != "Action first." || full.DisableModelInvocation != tc.disabled {
				t.Fatalf("explicit Load = %+v", full)
			}
			full, err = r.LoadForModel("style")
			if tc.disabled {
				if err == nil || full != nil {
					t.Fatalf("LoadForModel must reject manual-only skill: full=%+v, err=%v", full, err)
				}
			} else if err != nil || full == nil || full.Body != "Action first." {
				t.Fatalf("LoadForModel should load enabled skill: full=%+v, err=%v", full, err)
			}
		})
	}
}

func TestReader_LoadForModel_UsesCurrentPolicyWithCachedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initial  string
		current  string
		disabled bool
	}{
		{"disable-after-cache", "false", "true", true},
		{"enable-after-cache", "true", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			fm := "name: style\ndescription: output style\ndisable-model-invocation: "
			writeSkill(t, tmp, "style", fm+tc.initial, "Original instructions.")
			r := NewReader([]string{tmp}, zap.NewNop())
			if _, err := r.Load("style"); err != nil {
				t.Fatalf("prime metadata cache: %v", err)
			}
			writeSkill(t, tmp, "style", fm+tc.current, "Updated instructions.")
			full, err := r.LoadForModel("style")
			if tc.disabled {
				if err == nil || full != nil {
					t.Fatalf("new policy must prevent loading: full=%+v, err=%v", full, err)
				}
			} else if err != nil || full == nil || full.Body != "Updated instructions." {
				t.Fatalf("new policy must allow loading: full=%+v, err=%v", full, err)
			}
			full, err = r.Load("style")
			if err != nil {
				t.Fatalf("explicit Load after edit: %v", err)
			}
			if full.Body != "Updated instructions." || full.DisableModelInvocation != tc.disabled {
				t.Fatalf("explicit Load returned stale content or policy: %+v", full)
			}
		})
	}
}

func TestLoader_ManualOnlySkillRetainsExplicitPrompt(t *testing.T) {
	tmp := t.TempDir()
	writeSkill(t, tmp, "i-have-adhd",
		"name: i-have-adhd\ndescription: output style\ndisable-model-invocation: true", "Action first.")
	result := NewLoader([]string{tmp}, zap.NewNop()).LoadAll()
	if len(result.Errors) != 0 || len(result.Commands) != 1 {
		t.Fatalf("LoadAll = %+v", result)
	}
	pc := result.Commands[0].Prompt
	if pc == nil || !pc.UserInvocable || !pc.DisableModelInvocation {
		t.Fatalf("manual-only command policy lost: %+v", pc)
	}
	blocks, err := pc.GetPromptForCommand("", nil)
	if err != nil || len(blocks) != 1 || blocks[0].Text != "Action first." {
		t.Fatalf("explicit prompt expansion = %+v, err=%v", blocks, err)
	}
}

func TestSkillCard_JSON_OmitsModelInvocationPolicy(t *testing.T) {
	card := SkillCard{Name: "style", DisableModelInvocation: true}
	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "invocation") {
		t.Fatalf("model-visible JSON exposes internal invocation policy: %s", encoded)
	}
}
