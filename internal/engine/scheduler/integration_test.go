package scheduler_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	loopruntime "harnessclaw-go/internal/engine/loop/runtime"
	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/scheduler/diskout"
	"harnessclaw-go/internal/engine/scheduler/emit"
	"harnessclaw-go/internal/engine/scheduler/middlewares"
	asyncstrat "harnessclaw-go/internal/engine/scheduler/strategies/async"
	syncstrat "harnessclaw-go/internal/engine/scheduler/strategies/sync_"
	"harnessclaw-go/internal/engine/scheduler/tasks"
	"harnessclaw-go/internal/engine/session"
	legacyagent "harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/internal/provider/mock"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/tools"
	pkgtypes "harnessclaw-go/pkg/types"
)

// TestDispatcher_E2E_SyncRoundtrip 走完整的 scheduler.Dispatch 链路：
//
//	Dispatcher → middlewares.{Identity, AgentContext, TaskRegister, Analytics}
//	          → SyncStrategy.Spawn
//	          → Runtime.LLM.Run
//	          → loop.Run → MockProvider
//	          → SyncStrategy.accumulate → SyncOutcome
//
// 验证 SyncOutcome.Content 携带 MockProvider 返回的文本，Result.AgentID/TaskID
// 由 Identity middleware 分配。任何一环断裂都会失败。
func TestDispatcher_E2E_SyncRoundtrip(t *testing.T) {
	prov := mock.New(mock.Response{
		Text:       "integration ok",
		StopReason: "end_turn",
		Usage:      &pkgtypes.Usage{InputTokens: 7, OutputTokens: 4},
	})

	sessMgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	_, _ = sessMgr.GetOrCreate(context.Background(), "root", "ws", "u")

	rt := loopruntime.NewLLM(loopruntime.LLMArgs{
		Provider:      prov,
		Registry:      tool.NewRegistry(),
		SessionMgr:    sessMgr,
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Retryer:       retry.New(nil, zap.NewNop()),
		Logger:        zap.NewNop(),
		Cfg: loopruntime.Config{
			MaxTokens:     1024,
			ContextWindow: 8192,
			ToolTimeout:   30 * time.Second,
			RootDir:       t.TempDir(),
		},
	})

	deps := scheduler.Deps{
		Runtime:    rt,
		TaskMgr:    tasks.NewMemory(),
		DiskOutput: diskout.NewFS(t.TempDir()),
		Bus:        emit.NewMemory(),
		Log:        zap.NewNop(),
	}

	sched := scheduler.NewDispatcher(
		deps,
		middlewares.DefaultChain(deps),
		asyncstrat.New(deps),
		syncstrat.New(deps),
	)

	def := legacyagent.AgentDefinition{
		Name:      "smoke",
		AgentType: tool.AgentTypeSync,
		Profile:   "worker",
	}

	res, err := sched.Dispatch(context.Background(), scheduler.SpawnParams{
		Definition: def,
		Prompt:     "ping",
		Name:       "smoke-agent",
		InvokedBy:  scheduler.Invoker{Kind: scheduler.InvokerUserCommand, Source: "test"},
	})
	if err != nil {
		t.Fatalf("Dispatch err: %v", err)
	}

	// Result 基本字段
	if res.AgentID == "" {
		t.Error("AgentID empty — Identity middleware didn't fire")
	}
	if res.TaskID == "" {
		t.Error("TaskID empty — Identity middleware didn't fire")
	}
	if res.Strategy != "sync" {
		t.Errorf("Strategy = %q want sync", res.Strategy)
	}
	if res.Status != scheduler.StatusCompleted {
		t.Errorf("Status = %q want completed", res.Status)
	}

	// Outcome 是 SyncOutcome，Content 包含 mock 响应
	sync, ok := res.Outcome.(scheduler.SyncOutcome)
	if !ok {
		t.Fatalf("Outcome type = %T want SyncOutcome", res.Outcome)
	}

	var allText strings.Builder
	for _, b := range sync.Content {
		if b.Type == pkgtypes.ContentTypeText {
			allText.WriteString(b.Text)
		}
	}
	if !strings.Contains(allText.String(), "integration ok") {
		t.Errorf("Content text = %q want contains 'integration ok'", allText.String())
	}

	// Terminal 应该是 completed（end_turn 由 StopOnEndTurn hook 转换）
	if sync.Terminal.Reason != pkgtypes.TerminalCompleted {
		t.Errorf("Terminal.Reason = %q want completed", sync.Terminal.Reason)
	}
}

// TestDispatcher_E2E_AnalyticsBus 验证 Analytics middleware 在端到端路径里
// 真的把 spawn.started / spawn.completed 推到 emit.Bus。
func TestDispatcher_E2E_AnalyticsBus(t *testing.T) {
	prov := mock.New(mock.Response{Text: "ok", StopReason: "end_turn"})
	sessMgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	_, _ = sessMgr.GetOrCreate(context.Background(), "root", "ws", "u")

	rt := loopruntime.NewLLM(loopruntime.LLMArgs{
		Provider: prov, Registry: tool.NewRegistry(), SessionMgr: sessMgr,
		PromptBuilder: prompt.NewBuilder(prompt.NewRegistry(), zap.NewNop()),
		Retryer:       retry.New(nil, zap.NewNop()), Logger: zap.NewNop(),
		Cfg: loopruntime.Config{ContextWindow: 4096, RootDir: t.TempDir()},
	})
	bus := emit.NewMemory()

	topics := make(chan string, 4)
	bus.Subscribe(scheduler.TopicSpawnStarted, func(_ context.Context, e emit.Event) {
		topics <- e.Topic
	})
	bus.Subscribe(scheduler.TopicSpawnCompleted, func(_ context.Context, e emit.Event) {
		topics <- e.Topic
	})

	deps := scheduler.Deps{
		Runtime: rt, TaskMgr: tasks.NewMemory(),
		DiskOutput: diskout.NewFS(t.TempDir()), Bus: bus, Log: zap.NewNop(),
	}
	sched := scheduler.NewDispatcher(deps, middlewares.DefaultChain(deps),
		asyncstrat.New(deps), syncstrat.New(deps))

	_, err := sched.Dispatch(context.Background(), scheduler.SpawnParams{
		Definition: legacyagent.AgentDefinition{Name: "x", AgentType: tool.AgentTypeSync, Profile: "worker"},
		Prompt:     "ping",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]bool)
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case topic := <-topics:
			got[topic] = true
		case <-timeout:
			t.Fatalf("only got topics: %v want started + completed", got)
		}
	}
	if !got[scheduler.TopicSpawnStarted] || !got[scheduler.TopicSpawnCompleted] {
		t.Errorf("missing topics: %v", got)
	}
}
