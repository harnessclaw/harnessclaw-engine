package mention_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/legacy/agent"
	"harnessclaw-go/internal/engine/agent/runAgent/agentrun"
	"harnessclaw-go/internal/legacy/mention"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/legacy/spawn"
	"harnessclaw-go/internal/memory"
	"harnessclaw-go/pkg/types"
)

func TestTryRoute_NoMention_ReturnsNil(t *testing.T) {
	reg := agent.NewAgentDefinitionRegistry()
	reg.RegisterBuiltins()
	r := mention.NewRouter(agentrun.New(spawn.NewSpawner(zap.NewNop())), reg, agent.NewMentionParser(reg))

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
