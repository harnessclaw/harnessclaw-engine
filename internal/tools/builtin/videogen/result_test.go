package videogen

import (
	"context"
	"path/filepath"
	"testing"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
)

func TestErrResult(t *testing.T) {
	t.Parallel()
	r := errResult("boom", types.ToolErrorInvalidInput)
	if !r.IsError || r.Content != "boom" || r.ErrorType != types.ToolErrorInvalidInput {
		t.Fatalf("errResult mismatch: %+v", r)
	}
}

func TestResolveSessionRootFromScope(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sid := "sess-1"
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: workspace.SessionRoot(root, sid),
	})
	got, err := resolveSessionRoot(ctx, root)
	if err != nil {
		t.Fatalf("resolveSessionRoot: %v", err)
	}
	if got != workspace.SessionRoot(root, sid) {
		t.Fatalf("session root = %q", got)
	}
}

func TestResolveSessionRootMissing(t *testing.T) {
	t.Parallel()
	if _, err := resolveSessionRoot(context.Background(), ""); err == nil {
		t.Fatal("missing session root must error")
	}
}

func TestResolveOutDir_PrefersTaskDir(t *testing.T) {
	t.Parallel()
	sessionRoot := "/ws/session/sid"
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{
		SessionRoot: sessionRoot,
		TaskID:      "t-abc-123",
	})
	got := resolveOutDir(ctx, sessionRoot)
	want := filepath.Join(sessionRoot, "tasks", "t-abc-123")
	if got != want {
		t.Errorf("with TaskID: got %q, want %q", got, want)
	}
}

func TestResolveOutDir_FallsBackToGenerated(t *testing.T) {
	t.Parallel()
	sessionRoot := "/ws/session/sid"

	// 无 AgentScope
	got := resolveOutDir(context.Background(), sessionRoot)
	want := filepath.Join(sessionRoot, generatedDirName)
	if got != want {
		t.Errorf("no scope: got %q, want %q", got, want)
	}

	// 有 AgentScope 但 TaskID 空
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: sessionRoot})
	if got := resolveOutDir(ctx, sessionRoot); got != want {
		t.Errorf("empty TaskID: got %q, want %q", got, want)
	}
}

func TestEmitDeliverable(t *testing.T) {
	t.Parallel()
	events := make(chan types.EngineEvent, 1)
	ctx := tool.WithEventOut(context.Background(), events)
	emitDeliverable(ctx, "/tmp/video-x.mp4", 1234)
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventDeliverable || ev.Deliverable == nil ||
			ev.Deliverable.FilePath != "/tmp/video-x.mp4" || ev.Deliverable.ByteSize != 1234 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	default:
		t.Fatal("expected a deliverable event")
	}
}
