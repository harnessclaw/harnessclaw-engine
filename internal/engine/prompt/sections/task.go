package sections

import (
	"fmt"
	"strings"

	"harnessclaw-go/internal/engine/prompt"
)

// TaskSection renders the current task state: goal, plan, progress, blockers.
type TaskSection struct{}

func NewTaskSection() *TaskSection {
	return &TaskSection{}
}

func (s *TaskSection) Name() string     { return "task" }
func (s *TaskSection) Priority() int    { return 40 }
func (s *TaskSection) Cacheable() bool  { return false }
func (s *TaskSection) MinTokens() int   { return 20 }

func (s *TaskSection) Render(ctx *prompt.PromptContext, budget int) (string, error) {
	task := ctx.Task
	if task == nil || task.Goal == "" {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("# 当前任务状态\n\n")
	sb.WriteString(fmt.Sprintf("**目标**：%s\n\n", task.Goal))

	// Plan
	if len(task.Plan) > 0 {
		sb.WriteString("**计划**：\n")
		for i, step := range task.Plan {
			marker := "  "
			if i < task.CurrentStep {
				marker = "✓ "
			} else if i == task.CurrentStep {
				marker = "→ "
			}
			sb.WriteString(fmt.Sprintf("%s%d. %s\n", marker, i+1, step))
		}
		sb.WriteString("\n")
	}

	// Completed steps (if plan was re-planned and completed differs from plan)
	if len(task.CompletedSteps) > 0 && budget > 100 {
		sb.WriteString("**已完成**：\n")
		for _, step := range task.CompletedSteps {
			sb.WriteString(fmt.Sprintf("- %s\n", step))
		}
		sb.WriteString("\n")
	}

	// Blockers
	if len(task.Blockers) > 0 {
		sb.WriteString("**阻塞项**：\n")
		for _, b := range task.Blockers {
			sb.WriteString(fmt.Sprintf("- ⚠ %s\n", b))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("不要偏离这个目标。如果需要调整方向，说明原因。")

	return sb.String(), nil
}
