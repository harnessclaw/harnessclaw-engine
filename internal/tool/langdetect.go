package tool

import "strings"

// ExtToLanguage maps a file extension (including the leading dot) to a
// language identifier suitable for syntax highlighting. Returns "" for
// unrecognized extensions.
func ExtToLanguage(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".jsx":
		return "jsx"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt", ".kts":
		return "kotlin"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".scss":
		return "scss"
	case ".sql":
		return "sql"
	case ".md", ".markdown":
		return "markdown"
	case ".proto":
		return "protobuf"
	case ".dockerfile":
		return "dockerfile"
	case ".vue":
		return "vue"
	case ".php":
		return "php"
	case ".scala":
		return "scala"
	case ".lua":
		return "lua"
	case ".r":
		return "r"
	case ".dart":
		return "dart"
	case ".zig":
		return "zig"
	case ".nix":
		return "nix"
	case ".tf":
		return "hcl"
	case ".graphql", ".gql":
		return "graphql"
	default:
		return ""
	}
}
