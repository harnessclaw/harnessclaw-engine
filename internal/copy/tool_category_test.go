package copy

import "testing"

func TestCategorize(t *testing.T) {
	cases := []struct {
		name string
		want ToolCategory
	}{
		{"Write", CategoryWrite},
		{"Edit", CategoryWrite},
		{"MultiEdit", CategoryWrite},
		{"ArtifactWrite", CategoryWrite},
		{"Read", CategoryRead},
		{"Grep", CategoryRead},
		{"Glob", CategoryRead},
		{"LS", CategoryRead},
		{"BashOutput", CategoryRead},
		{"Bash", CategoryExec},
		{"WebSearch", CategoryNetwork},
		{"WebFetch", CategoryNetwork},
		{"TavilySearch", CategoryNetwork},
		{"Task", CategoryDispatch},
		{"Specialists", CategoryDispatch},
		{"SkillTool", CategoryDispatch},
		{"NonExistentTool", CategoryGeneric},
		{"", CategoryGeneric},
	}
	for _, c := range cases {
		if got := Categorize(c.name); got != c.want {
			t.Errorf("Categorize(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
