package loopruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/scheduler/runtime"
	"harnessclaw-go/internal/engine/session"
	legacyagent "harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/provider/mock"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	pkgtypes "harnessclaw-go/pkg/types"
)

// TestLLM_Smoke verifies the full Runtime.LLM → loop.Run → MockProvider chain:
//   - LLM Runtime 装配 loop.Config 正确
//   - loop.Run 调 MockProvider 并把响应通过 events channel 透传回 caller
//   - channel close 时刻和 loop.Run 返回同步
//
// 这是 scheduler 重构后唯一的"端到端 chain"测试 —— 如果它绿，emma →
// AgentTool → sched.Dispatch → SyncStrategy → Runtime → loop.Run 整链可信。
func TestLLM_Smoke(t *testing.T) {
	prov := mock.New(mock.Response{
		Text:       "hello back",
		StopReason: "end_turn",
		Usage:      &pkgtypes.Usage{InputTokens: 10, OutputTokens: 5},
	})

	sessMgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	// pre-create root session so BuildSubSession 能基于它派生子 session
	_, _ = sessMgr.GetOrCreate(context.Background(), "root", "ws-test", "u")

	toolReg := tool.NewRegistry()
	promptReg := prompt.NewRegistry()
	promptBuilder := prompt.NewBuilder(promptReg, zap.NewNop())

	retryer := retry.New(nil, zap.NewNop())

	rt := NewLLM(LLMArgs{
		Provider:      prov,
		Registry:      toolReg,
		SessionMgr:    sessMgr,
		Compactor:     nil,
		Retryer:       retryer,
		PromptBuilder: promptBuilder,
		Logger:        zap.NewNop(),
		Cfg: Config{
			MaxTokens:           1024,
			ContextWindow:       8192,
			ToolTimeout:         30 * time.Second,
			LLMAPITimeout:       30 * time.Second,
			LLMFirstByteTimeout: 30 * time.Second,
			RootDir:             t.TempDir(),
		},
	})

	def := legacyagent.AgentDefinition{
		Name:      "smoke-agent",
		AgentType: tool.AgentTypeSync,
		Profile:   "worker",
	}

	events, err := rt.Run(context.Background(), runtime.RunParams{
		AgentID:    "a-smoke-1",
		Definition: def,
		Prompt:     "please reply",
		Overrides:  runtime.Overrides{MaxTurns: 3},
	})
	if err != nil {
		t.Fatalf("Run returned err: %v", err)
	}

	// 收集 events 直到 channel close
	var collected []pkgtypes.EngineEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto done
			}
			collected = append(collected, evt)
		case <-timeout:
			t.Fatal("Run did not close events channel within 5s")
		}
	}
done:

	if len(collected) == 0 {
		t.Fatal("expected at least one event")
	}

	// 找文本帧：包含 mock provider 的响应
	foundText := false
	for _, evt := range collected {
		if strings.Contains(evt.Text, "hello back") {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Errorf("expected text event containing 'hello back'; got %d events; first 3: %+v",
			len(collected),
			firstN(collected, 3),
		)
	}
}

func firstN(evts []pkgtypes.EngineEvent, n int) []pkgtypes.EngineEvent {
	if len(evts) < n {
		return evts
	}
	return evts[:n]
}
