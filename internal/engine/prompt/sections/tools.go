package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/tool"
)

// ToolsSection renders available tool descriptions.
type ToolsSection struct{}

func NewToolsSection() *ToolsSection {
	return &ToolsSection{}
}

func (s *ToolsSection) Name() string     { return "tools" }
func (s *ToolsSection) Priority() int    { return 20 }
func (s *ToolsSection) Cacheable() bool  { return false }
func (s *ToolsSection) MinTokens() int   { return 200 }

func (s *ToolsSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	// Prefer the runtime-filtered tool set. Falling back to the full registry
	// is only safe when the caller hasn't applied an AllowedTools whitelist
	// (i.e. the LLM-visible schemas equal Tools.All()).
	var tools []tool.Tool
	if len(ctx.AvailableTools) > 0 {
		tools = ctx.AvailableTools
	} else if ctx.Tools != nil {
		tools = ctx.Tools.All()
	}
	if len(tools) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("# 可用工具\n\n")

	// Render tool summaries within budget.
	// Each tool gets: name + one-line description.
	tokensUsed := prompt.EstimateTokens("# 可用工具\n\n")
	for _, t := range tools {
		line := fmt.Sprintf("- **%s**：%s\n", t.Name(), t.Description())
		lineTokens := prompt.EstimateTokens(line)
		if tokensUsed+lineTokens > budget {
			sb.WriteString(fmt.Sprintf("\n_（还有 %d 个工具因预算限制未列出）_\n", len(tools)-countLines(sb.String())))
			break
		}
		sb.WriteString(line)
		tokensUsed += lineTokens
	}

	return sb.String(), nil
}

// countLines is a simple helper; counts non-empty lines for the omitted count.
func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "- **") {
			n++
		}
	}
	return n
}
