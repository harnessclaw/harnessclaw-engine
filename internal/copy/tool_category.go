// Package copy provides session-level localized copy strings for engine
// signals that need user-facing presentation (currently: tool card phase
// hints and the M4 "next round thinking" hint on message cards).
//
// Design philosophy: copy lives in the engine (not the front-end) so
// the wire carries resolved strings — front-end stays dumb and any
// future localization plumbs through this package, not React.
package copy

// ToolCategory groups tools by user-perceived behaviour. Used to pick
// category-appropriate copy templates (e.g. write-class tools get
// "已写入 N 字节" during args streaming; exec-class tools get "构造命令").
type ToolCategory string

const (
	CategoryWrite    ToolCategory = "write"
	CategoryRead     ToolCategory = "read"
	CategoryExec     ToolCategory = "exec"
	CategoryNetwork  ToolCategory = "network"
	CategoryDispatch ToolCategory = "dispatch"
	CategoryGeneric  ToolCategory = "generic"
)

// toolCategoryMap is the explicit name → category lookup. Adding a new
// tool requires explicit registration here — no prefix matching, no
// reflection. This protects against silent miscategorization when tool
// names overlap (e.g. "BashOutput" is read-class, not exec-class).
var toolCategoryMap = map[string]ToolCategory{
	"write":         CategoryWrite,
	"edit":          CategoryWrite,
	"MultiEdit":     CategoryWrite,
	"ArtifactWrite": CategoryWrite,

	"read":       CategoryRead,
	"grep":       CategoryRead,
	"glob":       CategoryRead,
	"LS":         CategoryRead,
	"BashOutput": CategoryRead,

	"bash": CategoryExec,

	"web_search":    CategoryNetwork,
	"web_fetch":     CategoryNetwork,
	"tavily_search": CategoryNetwork,

	"freelance":   CategoryDispatch,
	"scheduler":   CategoryDispatch,
	"SkillTool": CategoryDispatch,
}

// Categorize returns the registered category for a tool name. Unknown
// names (including empty strings) fall back to CategoryGeneric so the
// copy lookup always resolves to *something*.
func Categorize(toolName string) ToolCategory {
	if cat, ok := toolCategoryMap[toolName]; ok {
		return cat
	}
	return CategoryGeneric
}
