package videogen

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/internal/workspace"
	"harnessclaw-go/pkg/types"
	"go.uber.org/zap"
)

// scriptedProvider returns the i-th status per call then sticks on the last.
type scriptedProvider struct {
	stubProvider
	statuses []TaskStatus
	calls    int
	video    []byte
}

func (s *scriptedProvider) QueryTask(_ context.Context, _ QueryRequest) (*QueryResult, error) {
	idx := s.calls
	if idx >= len(s.statuses) {
		idx = len(s.statuses) - 1
	}
	s.calls++
	st := s.statuses[idx]
	res := &QueryResult{Status: st, Model: "m", Resolution: "720p", Ratio: "16:9", Duration: 5}
	if st == StatusSucceeded {
		res.VideoURL = "https://tos/video.mp4"
		res.URLExpiresAt = time.Unix(1000, 0)
	}
	if st == StatusFailed {
		res.ErrorCode = "ContentRiskNotPass"
		res.ErrorMessage = "blocked"
	}
	return res, nil
}
func (s *scriptedProvider) DownloadVideo(_ context.Context, _ string) ([]byte, string, error) {
	return s.video, "video/mp4", nil
}

func newQueryFixture(t *testing.T, p VideoProvider) (*VideoQueryTool, string, string) {
	t.Helper()
	reg := NewProviderRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	src := NewSource(newTestVideoCfg("sk-x", ""), fakeAgentSource{ref: "doubao:seedance-lite-i2v"})
	root := t.TempDir()
	q := NewQuery(src, reg, root, zap.NewNop())
	return q, root, "sess-q"
}

func queryCtx(t *testing.T, root, sid string) (context.Context, chan types.EngineEvent) {
	t.Helper()
	events := make(chan types.EngineEvent, 4)
	ctx := tool.WithAgentScope(context.Background(), tool.AgentScope{SessionRoot: workspace.SessionRoot(root, sid)})
	ctx = tool.WithEventOut(ctx, events)
	return ctx, events
}

func TestQuerySucceedsDownloadsAndEmits(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{stubProvider: stubProvider{name: "doubao"}, statuses: []TaskStatus{StatusRunning, StatusSucceeded}, video: []byte("MP4DATA")}
	q, root, sid := newQueryFixture(t, p)
	var vt time.Time
	q.now = func() time.Time { return vt }
	q.sleep = func(d time.Duration) { vt = vt.Add(d) }

	ctx, events := queryCtx(t, root, sid)
	res, err := q.Execute(ctx, json.RawMessage(`{"task_id":"cgt-1","timeout_s":10}`))
	if err != nil {
		t.Fatalf("go error: %v", err)
	}
	if res.IsError || res.Metadata["status"] != "succeeded" {
		t.Fatalf("expected success, got %+v", res)
	}
	path := filepath.Join(workspace.SessionRoot(root, sid), generatedDirName, "video-cgt-1.mp4")
	if data, err := os.ReadFile(path); err != nil || string(data) != "MP4DATA" {
		t.Fatalf("video file not written correctly: %v", err)
	}
	select {
	case ev := <-events:
		if ev.Type != types.EngineEventDeliverable {
			t.Fatalf("expected deliverable, got %v", ev.Type)
		}
	default:
		t.Fatal("expected a deliverable event")
	}
}

func TestQueryTimesOutReturnsRunning(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{stubProvider: stubProvider{name: "doubao"}, statuses: []TaskStatus{StatusRunning}}
	q, root, sid := newQueryFixture(t, p)
	var vt time.Time
	q.now = func() time.Time { return vt }
	q.sleep = func(d time.Duration) { vt = vt.Add(d) }

	ctx, _ := queryCtx(t, root, sid)
	res, _ := q.Execute(ctx, json.RawMessage(`{"task_id":"cgt-2","timeout_s":3}`))
	if res.IsError || res.Metadata["status"] != "running" {
		t.Fatalf("expected running, got %+v", res)
	}
	if p.calls < 4 {
		t.Fatalf("expected >=4 query calls (poll + final-check), got %d", p.calls)
	}
}

func TestQueryFailedReturnsError(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{stubProvider: stubProvider{name: "doubao"}, statuses: []TaskStatus{StatusFailed}}
	q, root, sid := newQueryFixture(t, p)
	ctx, _ := queryCtx(t, root, sid)
	res, _ := q.Execute(ctx, json.RawMessage(`{"task_id":"cgt-3"}`))
	if res.Metadata["status"] != "failed" || res.Metadata["error_code"] != "ContentRiskNotPass" {
		t.Fatalf("expected failed with code, got %+v", res)
	}
}

func TestQueryExpiredReturnsStatus(t *testing.T) {
	t.Parallel()
	p := &scriptedProvider{stubProvider: stubProvider{name: "doubao"}, statuses: []TaskStatus{StatusExpired}}
	q, root, sid := newQueryFixture(t, p)
	ctx, _ := queryCtx(t, root, sid)
	res, _ := q.Execute(ctx, json.RawMessage(`{"task_id":"cgt-4"}`))
	if res.Metadata["status"] != "expired" {
		t.Fatalf("expected expired, got %+v", res)
	}
}

func TestQueryIsLongRunning(t *testing.T) {
	t.Parallel()
	var _ tool.LongRunningTool = (*VideoQueryTool)(nil)
	p := &scriptedProvider{stubProvider: stubProvider{name: "doubao"}, statuses: []TaskStatus{StatusQueued}}
	q, _, _ := newQueryFixture(t, p)
	if !q.IsLongRunning() {
		t.Fatal("query must be long-running")
	}
}
