package worker

import (
	"context"

	"harnessclaw-go/internal/engine/agent/scheduler/spec"
	"harnessclaw-go/internal/engine/agent/scheduler/tstate"
	"harnessclaw-go/internal/engine/agent/scheduler/types"
	"harnessclaw-go/internal/msgbus"
	"harnessclaw-go/internal/legacy/workspace"
)

// LeafContext is the runtime context for one sub-agent execution.
//
// Provider and Tools remain typed as interface{} because the legacy
// stub path used in tests still references those positions; production
// code uses SpawnFn (set by Factory) to drive the real LLM↔tool loop.
type LeafContext struct {
	TaskID    types.TaskID
	SessionID string
	SpecRef   spec.TaskSpec
	Model     string
	Provider  interface{}
	Tools     interface{}
	Workspace WorkspaceHandle
	Staging   tstate.StagingWriter
	Bus       msgbus.Bus
	SpawnFn   SpawnFn
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
