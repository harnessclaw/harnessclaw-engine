package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
)

// EnvSection renders runtime environment information.
type EnvSection struct{}

func NewEnvSection() *EnvSection {
	return &EnvSection{}
}

func (s *EnvSection) Name() string     { return "env" }
func (s *EnvSection) Priority() int    { return 30 }
func (s *EnvSection) Cacheable() bool  { return false }
func (s *EnvSection) MinTokens() int   { return 20 }

func (s *EnvSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	env := ctx.EnvInfo

	var sb strings.Builder
	sb.WriteString("# 运行环境\n\n")
	sb.WriteString(fmt.Sprintf("- 平台：%s\n", env.Platform))
	sb.WriteString(fmt.Sprintf("- 操作系统：%s\n", env.OS))
	sb.WriteString(fmt.Sprintf("- Shell：%s\n", env.Shell))
	sb.WriteString(fmt.Sprintf("- 工作目录：%s\n", env.CWD))

	return sb.String(), nil
}
