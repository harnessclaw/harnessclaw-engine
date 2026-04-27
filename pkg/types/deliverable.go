package types

// Deliverable represents a file produced by a sub-agent during task execution.
// Detected automatically when a sub-agent calls FileWrite (render_hint: "file_info").
type Deliverable struct {
	// FilePath is the absolute path to the written file.
	FilePath string `json:"file_path"`

	// Language is the detected programming/markup language (from file extension).
	Language string `json:"language,omitempty"`

	// ByteSize is the number of bytes written.
	ByteSize int `json:"byte_size,omitempty"`

	// ToolUseID links back to the FileWrite tool call that created this deliverable.
	ToolUseID string `json:"tool_use_id,omitempty"`
}
