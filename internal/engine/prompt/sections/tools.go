package sections

import (
	"strings"

	"harnessclaw-go/internal/engine/prompt"
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
	if ctx.Tools == nil {
		return "", nil
	}

	tools := ctx.Tools.All()
	if len(tools) == 0 {
		return "", nil
	}

	// For now, render a simplified tools section
	// TODO: Implement full tool description rendering with budget awareness
	var sb strings.Builder
	sb.WriteString("# Using Your Tools\n\n")
	sb.WriteString("You have access to various tools for file operations, code search, and task management.\n\n")
	sb.WriteString("Important guidelines:\n")
	sb.WriteString("- Do NOT use Bash to run commands when a relevant dedicated tool is provided\n")
	sb.WriteString("- Use Read instead of cat, Edit instead of sed, Write instead of echo redirection\n")
	sb.WriteString("- Use Glob for file search, Grep for content search\n")
	sb.WriteString("- Break down complex work with tasks to track progress\n")

	return sb.String(), nil
}
