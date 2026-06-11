package tool

import "testing"

func TestExtToLanguage(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".go", "go"},
		{".py", "python"},
		{".js", "javascript"},
		{".ts", "typescript"},
		{".tsx", "tsx"},
		{".jsx", "jsx"},
		{".rs", "rust"},
		{".java", "java"},
		{".rb", "ruby"},
		{".c", "c"},
		{".h", "c"},
		{".cpp", "cpp"},
		{".hpp", "cpp"},
		{".cs", "csharp"},
		{".swift", "swift"},
		{".kt", "kotlin"},
		{".sh", "bash"},
		{".json", "json"},
		{".yaml", "yaml"},
		{".yml", "yaml"},
		{".toml", "toml"},
		{".xml", "xml"},
		{".html", "html"},
		{".css", "css"},
		{".sql", "sql"},
		{".md", "markdown"},
		{".proto", "protobuf"},
		{".vue", "vue"},
		{".php", "php"},
		{".dart", "dart"},
		{".zig", "zig"},
		{".nix", "nix"},
		{".tf", "hcl"},
		{".graphql", "graphql"},
		// Case insensitive.
		{".Go", "go"},
		{".PY", "python"},
		{".TSX", "tsx"},
		// Unknown.
		{".xyz", ""},
		{"", ""},
		{".unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			if got := ExtToLanguage(tt.ext); got != tt.want {
				t.Errorf("ExtToLanguage(%q) = %q, want %q", tt.ext, got, tt.want)
			}
		})
	}
}
