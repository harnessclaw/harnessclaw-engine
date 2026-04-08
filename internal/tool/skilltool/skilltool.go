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
}

// New creates a SkillTool backed by the given command registry.
func New(cmdReg *command.Registry) *SkillTool {
	return &SkillTool{cmdRegistry: cmdReg}
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
				"description": "The skill name. E.g., \"commit\", \"review-pr\", or \"pdf\"",
			},
			"args": map[string]any{
				"type":        "string",
				"description": "Optional arguments for the skill",
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
	var si skillInput
	if err := json.Unmarshal(input, &si); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	name := strings.TrimPrefix(si.Skill, "/")
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

	return &types.ToolResult{
		Content: fmt.Sprintf("Launching skill: %s", commandName),
		Metadata: map[string]any{
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

const skillToolDescription = `Execute a skill within the main conversation

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "pdf" - invoke the pdf skill
  - skill: "commit", args: "-m 'Fix bug'" - invoke with arguments
  - skill: "review-pr", args: "123" - invoke with arguments

Important:
- Available skills are listed in system-reminder messages in the conversation
- When a skill matches the user's request, this is a BLOCKING REQUIREMENT: invoke the relevant Skill tool BEFORE generating any other response about the task
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running
- Do not use this tool for built-in CLI commands (like /help, /clear, etc.)
- If you see a <command-name> tag in the current conversation turn, the skill has ALREADY been loaded - follow the instructions directly instead of calling this tool again`
