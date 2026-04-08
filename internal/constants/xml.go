// Package constants holds shared constants used across the codebase.
// Mirrors src/constants/xml.ts.
package constants

// XML tag names used to mark skill/command metadata in messages.
const (
	// CommandNameTag wraps the skill name in expanded prompts.
	CommandNameTag = "command-name"

	// CommandMessageTag wraps the command message content.
	CommandMessageTag = "command-message"

	// CommandArgsTag wraps the command arguments.
	CommandArgsTag = "command-args"
)
