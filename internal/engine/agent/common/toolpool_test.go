package common_test

import (
	"context"
	"encoding/json"
	"testing"

	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

type stubTool struct {
	tool.BaseTool
	name string
}

func (s *stubTool) Name() string                { return s.name }
func (s *stubTool) Description() string         { return "" }
func (s *stubTool) IsReadOnly() bool            { return true }
func (s *stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{Content: "ok"}, nil
}

func TestBuildToolPool_AllowedWhitelist(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(&stubTool{name: "read"})
	_ = reg.Register(&stubTool{name: "edit"})
	_ = reg.Register(&stubTool{name: "freelance"})

	pool := common.BuildToolPool(reg, []string{"read"}, tool.AgentTypeSync, false)

	if pool.Get("read") == nil {
		t.Error("expected 'read' in pool")
	}
	if pool.Get("edit") != nil {
		t.Error("expected 'edit' filtered out by AllowedTools whitelist")
	}
	if pool.Get("freelance") != nil {
		t.Error("expected 'freelance' filtered out")
	}
}

func TestBuildToolPool_StripDispatch(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(&stubTool{name: "read"})
	_ = reg.Register(&stubTool{name: "freelance"})
	_ = reg.Register(&stubTool{name: "scheduler"})

	pool := common.BuildToolPool(reg, []string{"read", "freelance"}, tool.AgentTypeSync, true)

	if pool.Get("read") == nil {
		t.Error("read should remain")
	}
	if pool.Get("freelance") != nil {
		t.Error("freelance should be stripped (TierSubAgent dispatch protection)")
	}
}
