package prompt

import (
	"strings"

	"harnessclaw-go/internal/skills"
)

// BuildLoadedSkillsBlock renders the <loaded-skills> XML container that
// SpawnSync prepends to a freelancer's first user message. Returns "" when
// the input is empty so callers don't have to nil-check.
//
// The format is identical to what load_skill injects via NewMessages later,
// so the LLM sees a single uniform shape for "skill body in messages":
//
//   <loaded-skills>
//   <skill name="..." version="..." root="...">
//   ...body...
//   </skill>
//   <skill ...>...</skill>
//   </loaded-skills>
func BuildLoadedSkillsBlock(fulls []*skill.SkillFull) string {
	if len(fulls) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<loaded-skills>\n")
	for _, f := range fulls {
		sb.WriteString(BuildSingleSkillBlock(f))
		sb.WriteString("\n")
	}
	sb.WriteString("</loaded-skills>")
	return sb.String()
}

// BuildSingleSkillBlock renders one <skill>...</skill> element. Exported
// because load_skill (in another package) needs to emit a single skill
// block for runtime hot-load NewMessages. Used by both Preload-time bulk
// injection (above) and load_skill-time NewMessages — same shape, no skew
// between paths.
func BuildSingleSkillBlock(f *skill.SkillFull) string {
	var sb strings.Builder
	sb.WriteString(`<skill name="`)
	sb.WriteString(f.Name)
	sb.WriteString(`"`)
	if f.Version != "" {
		sb.WriteString(` version="`)
		sb.WriteString(f.Version)
		sb.WriteString(`"`)
	}
	if f.Path != "" {
		sb.WriteString(` root="`)
		sb.WriteString(f.Path)
		sb.WriteString(`"`)
	}
	sb.WriteString(">\n")
	sb.WriteString(f.Body)
	sb.WriteString("\n</skill>")
	return sb.String()
}
