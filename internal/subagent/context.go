package subagent

import (
	"context"

	"harnessclaw-go/internal/engine/scheduler/spec"
	"harnessclaw-go/internal/engine/scheduler/tstate"
	"harnessclaw-go/internal/engine/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/workspace"
)

// LeafContext is the runtime context for one sub-agent execution.
//
// Phase 1 note: Provider and Tools are typed as interface{} because
// provider.Manager is a concrete struct (no interface yet) and tool.Executor
// does not exist yet. They will be typed properly in phase 2.2 when the real
// LLM loop is wired in.
type LeafContext struct {
	TaskID    types.TaskID
	SessionID string
	SpecRef   spec.TaskSpec
	Model     string
	Provider  interface{} // phase 2.2: provider.Manager
	Tools     interface{} // phase 2.2: tool.Executor
	Workspace WorkspaceHandle
	Staging   tstate.StagingWriter
	Bus       msgbus.Bus
}

// WorkspaceHandle is the filesystem-layer interface for a sub-agent.
// flat layout (react/planner/summarizer): TaskDir() == sessionRoot
// per-task layout (plan step): TaskDir() == sessionRoot/tasks/{tid}/
type WorkspaceHandle interface {
	TaskDir() string
	MetaPath() string
	MetaRelPath() string
	ReadScope() []string
	WriteScope() []string
	InputPaths() []string
	WriteFile(ctx context.Context, relPath string, data []byte) error
	ReadFile(ctx context.Context, relPath string) ([]byte, error)
	WriteMeta(ctx context.Context, m workspace.Meta) (relPath string, err error)
}
