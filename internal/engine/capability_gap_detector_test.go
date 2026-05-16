package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
	"harnessclaw-go/pkg/types"
)

func TestShouldWarn(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		final    []string
		want     bool
	}{
		{
			name:     "both search runtime missing",
			declared: []string{"WebSearch", "TavilySearch", "ArtifactRead"},
			final:    []string{"ArtifactRead"},
			want:     true,
		},
		{
			name:     "websearch present in runtime",
			declared: []string{"WebSearch", "TavilySearch"},
			final:    []string{"WebSearch"},
			want:     false,
		},
		{
			name:     "tavilysearch present in runtime",
			declared: []string{"WebSearch", "TavilySearch"},
			final:    []string{"TavilySearch"},
			want:     false,
		},
		{
			name:     "def declares neither search tool",
			declared: []string{"Bash", "FsRead"},
			final:    []string{},
			want:     false,
		},
		{
			name:     "empty declared",
			declared: nil,
			final:    nil,
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWarn(tc.declared, tc.final); got != tc.want {
				t.Errorf("shouldWarn(%v, %v) = %v, want %v", tc.declared, tc.final, got, tc.want)
			}
		})
	}
}

// fakeEmitter is a thread-safe collector for events emitted by the
// detector via its EmitFunc closure. Lets unit tests skip channel
// plumbing entirely.
type fakeEmitter struct {
	calls int32
	out   chan types.EngineEvent
}

func newFakeEmitter() *fakeEmitter {
	return &fakeEmitter{out: make(chan types.EngineEvent, 16)}
}

func (f *fakeEmitter) Emit(ctx context.Context, ev types.EngineEvent) error {
	atomic.AddInt32(&f.calls, 1)
	f.out <- ev
	return nil
}

func (f *fakeEmitter) Count() int { return int(atomic.LoadInt32(&f.calls)) }

func TestSearchGapDetector_BasicEmit(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, []string{"ArtifactRead"}, f.Emit)
	if f.Count() != 1 {
		t.Fatalf("expected 1 emit, got %d", f.Count())
	}
	ev := <-f.out
	if ev.Type != types.EngineEventSystemNotice {
		t.Errorf("event type: %v", ev.Type)
	}
	if ev.SystemNotice == nil {
		t.Fatal("system notice payload missing")
	}
	if ev.SystemNotice.Topic != "search_capability_gap" {
		t.Errorf("topic: %q", ev.SystemNotice.Topic)
	}
	if ev.SystemNotice.Title != "搜索能力不可用" {
		t.Errorf("title: %q", ev.SystemNotice.Title)
	}
	if ev.SystemNotice.Icon != "warning" {
		t.Errorf("icon: %q", ev.SystemNotice.Icon)
	}
	if !strings.Contains(ev.SystemNotice.Summary, "researcher") {
		t.Errorf("summary missing agent name: %q", ev.SystemNotice.Summary)
	}
	if !strings.Contains(ev.SystemNotice.ActionHint, "tools.web_search.enabled") {
		t.Errorf("action hint missing remediation: %q", ev.SystemNotice.ActionHint)
	}
}

func TestSearchGapDetector_DedupePerSession(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	for i := 0; i < 3; i++ {
		d.CheckAndEmit(context.Background(), "s1", "researcher",
			[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	}
	if f.Count() != 1 {
		t.Errorf("expected 1 emit (dedupe per session), got %d", f.Count())
	}
}

func TestSearchGapDetector_DifferentSessions(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	d.CheckAndEmit(context.Background(), "s2", "writer",
		[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	if f.Count() != 2 {
		t.Errorf("expected 2 emits across sessions, got %d", f.Count())
	}
}

func TestSearchGapDetector_NoSessionID(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	d.CheckAndEmit(context.Background(), "", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	if f.Count() != 0 {
		t.Errorf("expected 0 emits with empty session ID, got %d", f.Count())
	}
}

func TestSearchGapDetector_ForgetReEmits(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	d.Forget("s1")
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, f.Emit)
	if f.Count() != 2 {
		t.Errorf("expected 2 emits after Forget, got %d", f.Count())
	}
}

func TestSearchGapDetector_NilReceiverSafe(t *testing.T) {
	var d *SearchGapDetector
	// Should not panic.
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, func(_ context.Context, ev types.EngineEvent) error { return nil })
	d.Forget("s1")
}

func TestSearchGapDetector_NoWarnWhenSearchPresent(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	f := newFakeEmitter()
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, []string{"WebSearch"}, f.Emit)
	if f.Count() != 0 {
		t.Errorf("expected 0 emits when WebSearch available, got %d", f.Count())
	}
}

func TestSearchGapDetector_EmitFailureRollsBack(t *testing.T) {
	d := NewSearchGapDetector(zap.NewNop())
	failOnce := true
	var callCount int
	emit := func(_ context.Context, ev types.EngineEvent) error {
		callCount++
		if failOnce {
			failOnce = false
			return errStub("channel full")
		}
		return nil
	}
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, emit)
	d.CheckAndEmit(context.Background(), "s1", "researcher",
		[]string{"WebSearch", "TavilySearch"}, nil, emit)
	if callCount != 2 {
		t.Errorf("expected 2 emit attempts (1 fail + 1 retry), got %d", callCount)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
