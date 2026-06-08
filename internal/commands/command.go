package command

import "context"

// CommandType classifies command execution mode.
type CommandType string

const (
	CommandTypePrompt CommandType = "prompt"
	CommandTypeLocal  CommandType = "local"
)

// CommandSource defines where a command was loaded from, with explicit priority.
// Lower iota = higher priority.
type CommandSource int

const (
	SourceManaged     CommandSource = iota // Managed (policy) skills — highest priority
	SourceBundled                          // Bundled skills
	SourcePluginSkill                      // Plugin-provided skills
	SourceSkillDir                         // User skill directories
	SourceWorkflow                         // Workflow commands
	SourcePluginCmd                        // Plugin commands
	SourceBuiltin                          // Built-in commands — lowest priority
)

// String returns the human-readable name of a CommandSource.
func (cs CommandSource) String() string {
	switch cs {
	case SourceManaged:
		return "managed"
	case SourceBundled:
		return "bundled"
	case SourcePluginSkill:
		return "plugin_skill"
	case SourceSkillDir:
		return "skills"
	case SourceWorkflow:
		return "workflow"
	case SourcePluginCmd:
		return "plugin_command"
	case SourceBuiltin:
		return "builtin"
	default:
		return "unknown"
	}
}

// LoadedFrom tracks the origin type for security policy decisions.
type LoadedFrom string

const (
	LoadedFromSkills   LoadedFrom = "skills"
	LoadedFromCommands LoadedFrom = "commands_DEPRECATED"
	LoadedFromPlugin   LoadedFrom = "plugin"
	LoadedFromManaged  LoadedFrom = "managed"
	LoadedFromBundled  LoadedFrom = "bundled"
	LoadedFromMCP      LoadedFrom = "mcp"
)

// CommandBase holds fields common to all command types.
// Mirrors TypeScript's CommandBase from src/types/command.ts.
type CommandBase struct {
	// Name is the unique command identifier (e.g., "commit", "review-pr").
	Name string
	// Description is shown to users and the model.
	Description string
	// Aliases are alternative names for the command.
	Aliases []string
	// Source determines merge priority (lower = higher priority).
	Source CommandSource
	// LoadedFrom tracks the origin type.
	LoadedFrom LoadedFrom
	// IsEnabled indicates if the command is currently active.
	IsEnabled bool
	// IsHidden hides the command from typeahead/help.
	IsHidden bool
	// WhenToUse provides detailed usage scenarios for the model.
	WhenToUse string
	// Version is the command version string.
	Version string
	// UserInvocable indicates if users can invoke via /command-name. Default true.
	UserInvocable bool
	// DisableModelInvocation prevents the model from invoking via SkillTool.
	DisableModelInvocation bool
	// ArgumentHint is displayed in gray after the command name.
	ArgumentHint string
}

// ContentBlock represents a block of content in a prompt expansion.
type ContentBlock struct {
	Type string // "text"
	Text string
}

// PromptContext provides context for prompt command execution.
type PromptContext struct {
	SessionID string
	Cwd       string
	UserID    string
}

// PromptCommand expands text into the conversation (type = "prompt").
// This is the primary command type for skills.
type PromptCommand struct {
	CommandBase
	// GetPromptForCommand generates the prompt content for this command.
	GetPromptForCommand func(args string, ctx *PromptContext) ([]ContentBlock, error)
	// AllowedTools restricts which tools the model can use when processing this command.
	AllowedTools []string
	// Model overrides the model for this command.
	Model string
	// Effort overrides the reasoning effort level.
	Effort string
	// Context is "" for inline execution, "fork" for forked sub-agent execution.
	Context string
	// Agent specifies the agent type for forked execution.
	Agent string
	// ArgNames defines named argument placeholders.
	ArgNames []string
	// Paths are glob patterns for conditional activation.
	Paths []string
	// SkillRoot is the filesystem path to the skill directory.
	SkillRoot string
	// ContentLength is the byte length of the prompt body, used for token estimation.
	ContentLength int
}

// LocalCommand runs Go code and returns text (type = "local").
type LocalCommand struct {
	CommandBase
	// Execute runs the command and returns the result text.
	Execute func(args string, ctx context.Context) (string, error)
}

// Command is the unified command type. Only one of Prompt/Local is set.
type Command struct {
	Type   CommandType
	Prompt *PromptCommand // set when Type == CommandTypePrompt
	Local  *LocalCommand  // set when Type == CommandTypeLocal
}

// GetName returns the command name regardless of type.
func (c *Command) GetName() string {
	switch c.Type {
	case CommandTypePrompt:
		if c.Prompt != nil {
			return c.Prompt.Name
		}
	case CommandTypeLocal:
		if c.Local != nil {
			return c.Local.Name
		}
	}
	return ""
}

// GetBase returns the CommandBase regardless of type.
func (c *Command) GetBase() *CommandBase {
	switch c.Type {
	case CommandTypePrompt:
		if c.Prompt != nil {
			return &c.Prompt.CommandBase
		}
	case CommandTypeLocal:
		if c.Local != nil {
			return &c.Local.CommandBase
		}
	}
	return nil
}
