package engine

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"harnessclaw-go/internal/provider"
	"harnessclaw-go/pkg/types"
)

// streamPlanningProv emits one tool_use block over several chunks so we
// can verify Planning fires on first chunk + Progress fires above 200B.
type streamPlanningProv struct {
	chunks []types.StreamEvent
}

func (p *streamPlanningProv) Name() string { return "stream-planning" }

func (p *streamPlanningProv) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	ch := make(chan types.StreamEvent, len(p.chunks)+1)
	go func() {
		defer close(ch)
		for _, c := range p.chunks {
			ch <- c
		}
		ch <- types.StreamEvent{Type: types.StreamEventMessageEnd, StopReason: "tool_use"}
	}()
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func (p *streamPlanningProv) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 1, nil
}

func TestCallLLMOnce_EmitsToolPlanningOnFirstChunk(t *testing.T) {
	prov := &streamPlanningProv{
		chunks: []types.StreamEvent{
			{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{
					ID:    "toolu_1",
					Name:  "Bash",
					Input: `{"command":"ls"`, // 50B 不到 200B
				},
			},
		},
	}

	planningOut := make(chan types.EngineEvent, 8)
	callLLMOnce(context.Background(), prov, &provider.ChatRequest{}, nil, planningOut, llmCallTimeouts{}, zap.NewNop())
	close(planningOut)

	var sawPlanning bool
	var sawProgress bool
	for ev := range planningOut {
		if ev.Type == types.EngineEventToolPlanning && ev.ToolUseID == "toolu_1" && ev.ToolName == "Bash" {
			sawPlanning = true
		}
		if ev.Type == types.EngineEventToolPlanningProgress {
			sawProgress = true
		}
	}
	if !sawPlanning {
		t.Error("expected EngineEventToolPlanning to be emitted")
	}
	if sawProgress {
		t.Error("did not expect EngineEventToolPlanningProgress for < 200B args")
	}
}

func TestCallLLMOnce_EmitsProgressAboveThreshold(t *testing.T) {
	// Build a >= 200B input string
	bigContent := makeStringLLMCall(300)
	bigInput := `{"path":"/tmp/x","content":"` + bigContent + `"}`
	prov := &streamPlanningProv{
		chunks: []types.StreamEvent{
			{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{ID: "toolu_big", Name: "Write", Input: bigInput},
			},
		},
	}

	planningOut := make(chan types.EngineEvent, 8)
	callLLMOnce(context.Background(), prov, &provider.ChatRequest{}, nil, planningOut, llmCallTimeouts{}, zap.NewNop())
	close(planningOut)

	var planningCount, progressCount int
	var lastBytes int
	for ev := range planningOut {
		switch ev.Type {
		case types.EngineEventToolPlanning:
			planningCount++
		case types.EngineEventToolPlanningProgress:
			progressCount++
			lastBytes = ev.Bytes
		}
	}
	if planningCount != 1 {
		t.Errorf("planning count = %d, want 1", planningCount)
	}
	if progressCount == 0 {
		t.Error("expected at least one Progress event for > 200B args")
	}
	if lastBytes < 200 {
		t.Errorf("lastBytes = %d, want >= 200", lastBytes)
	}
}

func makeStringLLMCall(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
