package copy

import "testing"

func TestCategorize(t *testing.T) {
	cases := []struct {
		name string
		want ToolCategory
	}{
		{"write", CategoryWrite},
		{"edit", CategoryWrite},
		{"MultiEdit", CategoryWrite},
		{"ArtifactWrite", CategoryWrite},
		{"read", CategoryRead},
		{"grep", CategoryRead},
		{"glob", CategoryRead},
		{"LS", CategoryRead},
		{"BashOutput", CategoryRead},
		{"bash", CategoryExec},
		{"web_search", CategoryNetwork},
		{"web_fetch", CategoryNetwork},
		{"tavily_search", CategoryNetwork},
		{"task", CategoryDispatch},
		{"scheduler", CategoryDispatch},
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
