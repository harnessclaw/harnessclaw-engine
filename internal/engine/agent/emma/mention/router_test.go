package mention_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/emma/mention"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/pkg/types"
)

// fakeScheduler 在不命中 mention 的 unit test 里仅用作占位 ——
// TryRoute 在 non-mention 路径上根本不调 Dispatch / Subscribe。
type fakeScheduler struct{}

func (fakeScheduler) Dispatch(context.Context, scheduler.SpawnParams) (scheduler.Result, error) {
	return scheduler.Result{}, nil
}
func (fakeScheduler) Subscribe(context.Context, types.TaskID) (<-chan types.EngineEvent, error) {
	return nil, scheduler.ErrNotSubscribable
}

func TestTryRoute_NoMention_ReturnsNil(t *testing.T) {
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	r := mention.NewRouter(fakeScheduler{}, reg, agent.NewMentionParser(reg))

	mgr := session.NewManager(memory.New(), zap.NewNop(), time.Hour)
	sess, _ := mgr.GetOrCreate(context.Background(), "s1", "ws", "u")

	msg := &types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello no mention"}},
	}
	got := r.TryRoute(context.Background(), sess, msg)
	if got != nil {
		t.Errorf("expected nil for non-mention message, got channel")
	}
}
