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
		t.Error("expected 'freelance' filtered out (not in whitelist)")
	}
}

// Regression: AllowedTools whitelist must bypass the AgentType blacklist.
// Pre-fix behavior: AgentTypeSync's AllAgentDisallowed stripped
// `freelance` before the whitelist ran, so L2 react LLM never saw the
// dispatch tool the scheduler principles told it to call — it then
// hallucinated `<freelance>...` markup that toolexec rejected with
// "unknown tool: freelance". Whitelist must be authoritative when set,
// matching the comments in tool/restrictions.go:93 and
// agent/definition.go scheduler block.
func TestBuildToolPool_WhitelistBypassesAgentTypeBlacklist(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(&stubTool{name: "read"})
	_ = reg.Register(&stubTool{name: "freelance"}) // in AllAgentDisallowed
	_ = reg.Register(&stubTool{name: "scheduler"}) // in AllAgentDisallowed

	pool := common.BuildToolPool(
		reg,
		[]string{"read", "freelance", "scheduler"},
		tool.AgentTypeSync, // would normally strip freelance + scheduler
		false,
	)

	if pool.Get("freelance") == nil {
		t.Error("freelance must survive: whitelist is authoritative over AgentType blacklist")
	}
	if pool.Get("scheduler") == nil {
		t.Error("scheduler must survive: whitelist is authoritative over AgentType blacklist")
	}
	if pool.Get("read") == nil {
		t.Error("read must survive")
	}
}

// When no whitelist is supplied, the AgentType blacklist still applies
// — preserves the leaf-agent default behavior.
func TestBuildToolPool_NoWhitelistAppliesBlacklist(t *testing.T) {
	reg := tool.NewRegistry()
	_ = reg.Register(&stubTool{name: "read"})
	_ = reg.Register(&stubTool{name: "freelance"})

	pool := common.BuildToolPool(reg, nil, tool.AgentTypeSync, false)

	if pool.Get("read") == nil {
		t.Error("read must survive (not in blacklist)")
	}
	if pool.Get("freelance") != nil {
		t.Error("freelance must be stripped by AgentTypeSync blacklist when no whitelist set")
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
