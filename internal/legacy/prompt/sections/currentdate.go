package sections

import (
	"fmt"

	"harnessclaw-go/internal/legacy/prompt"
)

// CurrentDateSection renders the current date as an epilogue at the end
// of the system prompt.
//
// Placement rationale: Anthropic prompt caching caches by prefix; a
// date at the top would invalidate the entire static prefix every day.
// Moving it to the epilogue tier (priority 90) keeps role / principles /
// tools / env / memory cached across day boundaries — only the tail
// segment is re-tokenized. The model still anchors relative-time
// references like 「今年」「近期」 reliably because the date sits in the
// system prompt regardless of position.
type CurrentDateSection struct{}

func NewCurrentDateSection() *CurrentDateSection {
	return &CurrentDateSection{}
}

func (s *CurrentDateSection) Name() string    { return "currentdate" }
func (s *CurrentDateSection) Priority() int   { return 90 }
func (s *CurrentDateSection) Cacheable() bool { return true }
func (s *CurrentDateSection) MinTokens() int  { return 5 }

func (s *CurrentDateSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	if ctx.EnvInfo.Date == "" {
		return "", nil
	}
	return fmt.Sprintf("重要：今天是 %s，当前年份是 %s 年。用户提到「今年」「近期」「最近」等相对时间时，以此日期为基准。", ctx.EnvInfo.Date, ctx.EnvInfo.Date[:4]), nil
}
