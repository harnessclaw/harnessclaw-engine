package llmcall

import (
	"context"
	"fmt"
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
					Name:  "bash",
					Input: `{"command":"ls"`, // 50B 不到 200B
				},
			},
		},
	}

	planningOut := make(chan types.EngineEvent, 8)
	CallLLMOnce(context.Background(), prov, &provider.ChatRequest{}, nil, planningOut, LLMCallTimeouts{}, zap.NewNop())
	close(planningOut)

	var sawPlanning bool
	var sawProgress bool
	for ev := range planningOut {
		if ev.Type == types.EngineEventToolPlanning && ev.ToolUseID == "toolu_1" && ev.ToolName == "bash" {
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
				ToolCall: &types.ToolCall{ID: "toolu_big", Name: "write", Input: bigInput},
			},
		},
	}

	planningOut := make(chan types.EngineEvent, 8)
	CallLLMOnce(context.Background(), prov, &provider.ChatRequest{}, nil, planningOut, LLMCallTimeouts{}, zap.NewNop())
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

func TestCallLLM_EmitsRetractOnRetry(t *testing.T) {
	prov := &retryMockProvider{
		failUntil: 1, // first attempt fails, second succeeds
		failErr:   fmt.Errorf("read tcp: i/o timeout"),
	}

	out := make(chan types.EngineEvent, 32)
	planningOut := make(chan types.EngineEvent, 32)

	result := CallLLM(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(),
		fastRetryer(3), LLMCallTimeouts{}, "main", out, planningOut)

	if result.StreamErr != nil {
		t.Fatalf("expected success on attempt 2, got %v", result.StreamErr)
	}

	close(out)
	close(planningOut)

	var sawRetract bool
	for ev := range planningOut {
		if ev.Type == types.EngineEventToolPlanningRetract {
			sawRetract = true
		}
	}
	if !sawRetract {
		t.Error("expected EngineEventToolPlanningRetract during retry")
	}
}

func TestCallLLM_EmitsToolQueuedOnSuccess(t *testing.T) {
	// Provider returns 1 tool_use + MessageEnd
	prov := &streamPlanningProv{
		chunks: []types.StreamEvent{
			{
				Type: types.StreamEventToolUse,
				ToolCall: &types.ToolCall{ID: "toolu_q", Name: "read", Input: `{"path":"/tmp/x"}`},
			},
		},
	}
	out := make(chan types.EngineEvent, 16)
	planningOut := make(chan types.EngineEvent, 16)

	result := CallLLM(context.Background(), prov, &provider.ChatRequest{}, zap.NewNop(),
		nil /* no retry */, LLMCallTimeouts{}, "main", out, planningOut)

	if result.StreamErr != nil {
		t.Fatalf("unexpected stream err: %v", result.StreamErr)
	}
	close(out)
	close(planningOut)

	var queuedCount int
	var queuedToolName string
	for ev := range planningOut {
		if ev.Type == types.EngineEventToolQueued {
			queuedCount++
			queuedToolName = ev.ToolName
		}
	}
	if queuedCount != 1 {
		t.Errorf("queuedCount = %d, want 1", queuedCount)
	}
	if queuedToolName != "read" {
		t.Errorf("queuedToolName = %q, want Read", queuedToolName)
	}
}
