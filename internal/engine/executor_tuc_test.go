package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// TestExecutor_InjectsToolUseContext asserts that ToolExecutor injects a
// populated ToolUseContext into the tool's execution context when the
// parent ctx carries a SessionID.
//
// Before the fix, tool.GetToolUseContext always returned nil, so scheduler
// could never read ParentSessionID and sub-agent token metrics were lost.
func TestExecutor_InjectsToolUseContext(t *testing.T) {
	// --- setup ---
	captured := &captureToolImpl{}

	reg := tool.NewRegistry()
	if err := reg.Register(captured); err != nil {
		t.Fatalf("register: %v", err)
	}
	pool := tool.NewToolPool(reg, nil, nil)

	logger := zap.NewNop()
	perm := permission.BypassChecker{}
	te := NewToolExecutor(pool, perm, logger, 5*time.Second, nil)

	// Attach a session ID so the executor will build the TUC.
	ctx := sessionstats.WithSessionID(context.Background(), "sess_emma_001")

	out := make(chan types.EngineEvent, 16)
	input := json.RawMessage(`{"x":1}`)
	tc := types.ToolCall{
		ID:    "tooluse_abc",
		Name:  "Capture",
		Input: string(input),
	}

	// --- act ---
	results := te.ExecuteBatch(ctx, []types.ToolCall{tc}, out)
	close(out)

	// --- assert tool ran without error ---
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("ExecuteBatch failed: %+v", results)
	}

	// --- assert TUC was injected ---
	tuc, ok := tool.GetToolUseContext(captured.gotCtx)
	if !ok || tuc == nil {
		t.Fatal("ToolUseContext not injected: GetToolUseContext returned nil/false")
	}

	if tuc.Core.SessionID != "sess_emma_001" {
		t.Errorf("TUC.Core.SessionID = %q, want %q", tuc.Core.SessionID, "sess_emma_001")
	}
	if tuc.Core.ToolCallID != "tooluse_abc" {
		t.Errorf("TUC.Core.ToolCallID = %q, want %q", tuc.Core.ToolCallID, "tooluse_abc")
	}
	if tuc.Core.ToolName != "Capture" {
		t.Errorf("TUC.Core.ToolName = %q, want %q", tuc.Core.ToolName, "Capture")
	}
	if string(tuc.Core.ToolInput) != `{"x":1}` {
		t.Errorf("TUC.Core.ToolInput = %q, want %q", string(tuc.Core.ToolInput), `{"x":1}`)
	}
}

// TestExecutor_NoTUCWhenNoSessionID verifies that when the ctx carries no
// SessionID the executor does not inject a ToolUseContext — the tool must
// not receive a partially-populated TUC with an empty session_id.
func TestExecutor_NoTUCWhenNoSessionID(t *testing.T) {
	captured := &captureToolImpl{}

	reg := tool.NewRegistry()
	if err := reg.Register(captured); err != nil {
		t.Fatalf("register: %v", err)
	}
	pool := tool.NewToolPool(reg, nil, nil)

	te := NewToolExecutor(pool, permission.BypassChecker{}, zap.NewNop(), 5*time.Second, nil)

	out := make(chan types.EngineEvent, 16)
	tc := types.ToolCall{ID: "tooluse_noctx", Name: "Capture", Input: `{"x":2}`}

	results := te.ExecuteBatch(context.Background(), []types.ToolCall{tc}, out)
	close(out)

	if len(results) != 1 || results[0].IsError {
		t.Fatalf("ExecuteBatch failed: %+v", results)
	}

	if tuc, ok := tool.GetToolUseContext(captured.gotCtx); ok && tuc != nil {
		t.Errorf("expected no TUC when no SessionID, got %+v", tuc)
	}
}
