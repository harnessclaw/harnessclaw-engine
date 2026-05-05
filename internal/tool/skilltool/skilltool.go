// Package skilltool implements the SkillTool that bridges the tool system
// and the command/skill system.
//
// The SkillTool is registered as a regular tool in the tool registry. When the
// LLM invokes it, it looks up the named skill in the command registry, executes
// the skill's prompt generator, and returns the expanded content.
//
// This mirrors src/tools/SkillTool/SkillTool.ts.
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/constants"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// ToolName is the registered name of the SkillTool.
const ToolName = "Skill"

// SkillTool bridges the tool system to the command/skill system.
type SkillTool struct {
	tool.BaseTool
	cmdRegistry *command.Registry
	logger      *zap.Logger
}

// New creates a SkillTool backed by the given command registry.
func New(cmdReg *command.Registry, logger *zap.Logger) *SkillTool {
	return &SkillTool{cmdRegistry: cmdReg, logger: logger}
}

func (s *SkillTool) Name() string        { return ToolName }
func (s *SkillTool) Description() string { return skillToolDescription }
func (s *SkillTool) IsReadOnly() bool     { return true }

func (s *SkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"skill": map[string]any{
				"type":        "string",
				"description": "skill 名字。例如 \"commit\"、\"review-pr\"、\"pdf\"。",
			},
			"args": map[string]any{
				"type":        "string",
				"description": "传给 skill 的可选参数。",
			},
		},
		"required": []string{"skill"},
	}
}

// skillInput is the parsed input for the SkillTool.
type skillInput struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

func (s *SkillTool) ValidateInput(input json.RawMessage) error {
	var si skillInput
	if err := json.Unmarshal(input, &si); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if si.Skill == "" {
		return fmt.Errorf("skill name is required")
	}

	// Normalize: strip leading /
	name := strings.TrimPrefix(si.Skill, "/")

	// Look up command.
	cmd := s.cmdRegistry.FindCommand(name)
	if cmd == nil {
		return fmt.Errorf("unknown skill: %s", name)
	}
	if cmd.Type != command.CommandTypePrompt {
		return fmt.Errorf("skill %s is not a prompt command", name)
	}
	if cmd.Prompt != nil && cmd.Prompt.DisableModelInvocation {
		return fmt.Errorf("skill %s cannot be invoked by the model", name)
	}
	return nil
}

func (s *SkillTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	startTime := time.Now()

	var si skillInput
	if err := json.Unmarshal(input, &si); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	name := strings.TrimPrefix(si.Skill, "/")

	// Check allowed skills whitelist (set by sub-agent context).
	if allowed, ok := tool.GetAllowedSkills(ctx); ok && !allowed[name] {
		return &types.ToolResult{
			Content: fmt.Sprintf("skill %s is not available for this agent", name),
			IsError: true,
		}, nil
	}

	cmd := s.cmdRegistry.FindCommand(name)
	if cmd == nil {
		return &types.ToolResult{Content: "unknown skill: " + name, IsError: true}, nil
	}
	if cmd.Type != command.CommandTypePrompt || cmd.Prompt == nil {
		return &types.ToolResult{Content: "skill " + name + " is not a prompt command", IsError: true}, nil
	}

	pc := cmd.Prompt

	// Execute the prompt generator.
	// Populate PromptContext from ToolUseContext if available.
	promptCtx := &command.PromptContext{}
	if tuc, ok := tool.GetToolUseContext(ctx); ok {
		promptCtx.SessionID = tuc.Core.SessionID
		promptCtx.Cwd = tuc.File.Cwd
		promptCtx.UserID = tuc.Core.UserID
	}
	blocks, err := pc.GetPromptForCommand(si.Args, promptCtx)
	if err != nil {
		s.logger.Warn("skill execution failed",
			zap.String("skill", name),
			zap.String("args", si.Args),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "skill execution failed: " + err.Error(), IsError: true}, nil
	}

	// Concatenate text blocks into the skill prompt content.
	var sb strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	skillPrompt := sb.String()

	// Matching TS behavior: tool_result is minimal ("Launching skill: <name>"),
	// and the actual skill prompt is injected as a separate user message via NewMessages.
	// This makes the model treat the skill prompt as an instruction (user role)
	// rather than as a tool output, significantly improving prompt adherence.
	commandName := pc.Name
	if commandName == "" {
		commandName = name
	}

	s.logger.Info("skill executed",
		zap.String("skill", name),
		zap.String("args", si.Args),
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("prompt_length", len(skillPrompt)),
		zap.String("context", pc.Context),
		zap.String("model", pc.Model),
	)

	return &types.ToolResult{
		Content: fmt.Sprintf("Launching skill: %s", commandName),
		Metadata: map[string]any{
			"render_hint":   "skill",
			"skill_name":    name,
			"allowed_tools": pc.AllowedTools,
			"model":         pc.Model,
			"effort":        pc.Effort,
			"context":       pc.Context,
		},
		NewMessages: []types.Message{
			{
				Role: types.RoleUser,
				Content: []types.ContentBlock{{
					Type: types.ContentTypeText,
					Text: fmt.Sprintf("<%s>%s</%s>\n%s", constants.CommandNameTag, commandName, constants.CommandNameTag, skillPrompt),
				}},
				CreatedAt: time.Now(),
			},
		},
	}, nil
}

// CheckPermission implements tool.PermissionPreChecker for SkillTool.
// Skills with only safe properties are auto-allowed.
func (s *SkillTool) CheckPermission(_ context.Context, input json.RawMessage) tool.PermissionPreResult {
	var si skillInput
	if err := json.Unmarshal(input, &si); err != nil {
		return tool.PermissionPreResult{Behavior: "passthrough"}
	}

	name := strings.TrimPrefix(si.Skill, "/")
	cmd := s.cmdRegistry.FindCommand(name)
	if cmd == nil || cmd.Prompt == nil {
		return tool.PermissionPreResult{Behavior: "passthrough"}
	}

	if HasOnlySafeProperties(cmd.Prompt) {
		return tool.PermissionPreResult{
			Behavior: "allow",
			Message:  "skill has only safe properties",
		}
	}

	return tool.PermissionPreResult{Behavior: "passthrough"}
}

const skillToolDescription = `在主对话中执行一个 skill。

用户让你做事时，先看看有没有匹配的可用 skill。Skill 提供专项能力和领域知识。

用户提到"slash 命令"或 "/<某个名字>"（如 "/commit"、"/review-pr"）就是在引用一个 skill。用本工具去调用它。

调用方式：
- 提供 skill 名字 + 可选 args。
- 示例：
  - skill: "pdf" — 调用 pdf skill
  - skill: "commit", args: "-m 'Fix bug'" — 带参数调用
  - skill: "review-pr", args: "123"

重要规则：
- 可用 skill 列表在 system-reminder 消息里。
- 用户请求匹配某个 skill 时，**强制要求**：先调用 Skill 工具，再做任何回应。
- 不要光提 skill 名字而不真的调用本工具。
- 不要重复调用一个正在跑的 skill。
- 不要把本工具用在内置 CLI 命令（如 /help、/clear）上。
- 如果当前对话已经出现 <command-name> 标签，说明 skill 已加载——按里面的指示直接执行，而不是再调一次本工具。`
