package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"harnessclaw-go/internal/config"
	tool "harnessclaw-go/internal/tools"
	"harnessclaw-go/pkg/types"
)

// --- test doubles -----------------------------------------------------------

type callRec struct {
	name string
	args map[string]any
}

type fakeSession struct {
	specs   []RemoteToolSpec
	listErr error
	closed  bool

	calls   []callRec
	result  *types.ToolResult
	callErr error
}

func newFakeSession(names ...string) *fakeSession {
	specs := make([]RemoteToolSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, RemoteToolSpec{Name: n, Description: n + " desc"})
	}
	return &fakeSession{specs: specs}
}

func (f *fakeSession) ListTools(context.Context) ([]RemoteToolSpec, error) {
	return f.specs, f.listErr
}

func (f *fakeSession) CallTool(_ context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	f.calls = append(f.calls, callRec{name: name, args: args})
	if f.callErr != nil {
		return nil, f.callErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &types.ToolResult{Content: "ok"}, nil
}

func (f *fakeSession) Close() error {
	f.closed = true
	return nil
}

type fakeConnector struct {
	sessions map[string]*fakeSession
	connErr  map[string]error
	seen     []ServerConfig
}

func (c *fakeConnector) Connect(_ context.Context, cfg ServerConfig) (Session, error) {
	c.seen = append(c.seen, cfg)
	if c.connErr != nil {
		if err := c.connErr[cfg.Name]; err != nil {
			return nil, err
		}
	}
	if s, ok := c.sessions[cfg.Name]; ok {
		return s, nil
	}
	return newFakeSession(), nil
}

// --- helpers ----------------------------------------------------------------

func names(ts []tool.Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}

// --- tests ------------------------------------------------------------------

func TestManagerLoadNamespacesAndAppliesPerServerDeny(t *testing.T) {
	fc := &fakeConnector{sessions: map[string]*fakeSession{
		"alpha": newFakeSession("read", "write"),
		"beta":  newFakeSession("read"),
	}}
	servers := []config.MCPServerConfig{
		{Name: "alpha", Deny: []string{"write"}},
		{Name: "beta"},
	}
	got := names(NewManager(servers, fc, nil).Load(context.Background()))

	want := map[string]bool{"alpha__read": true, "beta__read": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want keys %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected tool %q (denied or mis-namespaced)", n)
		}
	}
}

func TestManagerLoadSkipsDisabledEmptyAndDuplicate(t *testing.T) {
	fc := &fakeConnector{sessions: map[string]*fakeSession{
		"real": newFakeSession("t"),
		"dup":  newFakeSession("t"),
	}}
	servers := []config.MCPServerConfig{
		{Name: "off", Disabled: true, Command: "x"},
		{Name: "  "}, // empty after trim
		{Name: "real"},
		{Name: "dup"},
		{Name: "dup"}, // duplicate name — second skipped
	}
	fcm := NewManager(servers, fc, nil)
	got := names(fcm.Load(context.Background()))

	if len(got) != 2 {
		t.Fatalf("expected 2 tools (real + first dup), got %v", got)
	}
	for _, sc := range fc.seen {
		if sc.Name == "off" {
			t.Errorf("disabled server was connected")
		}
	}
}

func TestManagerLoadContinuesPastConnectFailure(t *testing.T) {
	fc := &fakeConnector{
		sessions: map[string]*fakeSession{"good": newFakeSession("ok")},
		connErr:  map[string]error{"bad": errors.New("boom")},
	}
	servers := []config.MCPServerConfig{{Name: "bad"}, {Name: "good"}}
	got := names(NewManager(servers, fc, nil).Load(context.Background()))

	if len(got) != 1 || got[0] != "good__ok" {
		t.Fatalf("expected only good__ok after bad server skipped, got %v", got)
	}
}

func TestManagerLoadSkipsServerWhoseListFails(t *testing.T) {
	failing := newFakeSession("x")
	failing.listErr = errors.New("nope")
	fc := &fakeConnector{sessions: map[string]*fakeSession{
		"fails": failing,
		"works": newFakeSession("y"),
	}}
	servers := []config.MCPServerConfig{{Name: "fails"}, {Name: "works"}}
	got := names(NewManager(servers, fc, nil).Load(context.Background()))

	if len(got) != 1 || got[0] != "works__y" {
		t.Fatalf("expected only works__y, got %v", got)
	}
	if !failing.closed {
		t.Error("session that failed ListTools should be closed")
	}
}

func TestManagerCloseClosesEverySession(t *testing.T) {
	a := newFakeSession("a")
	b := newFakeSession("b")
	fc := &fakeConnector{sessions: map[string]*fakeSession{"a": a, "b": b}}
	m := NewManager([]config.MCPServerConfig{{Name: "a"}, {Name: "b"}}, fc, nil)
	m.Load(context.Background())

	if err := m.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !a.closed || !b.closed {
		t.Errorf("expected both sessions closed, got a=%v b=%v", a.closed, b.closed)
	}
}

func TestRemoteToolExecuteForwardsArgsStripsIntentAndAttributes(t *testing.T) {
	sess := newFakeSession("do")
	tl := newRemoteTool("srv", RemoteToolSpec{Name: "do"}, sess, nil)

	if tl.Name() != "srv__do" {
		t.Fatalf("Name = %q, want srv__do", tl.Name())
	}
	if tl.ServerName() != "srv" || tl.RemoteName() != "do" {
		t.Fatalf("attribution accessors wrong: %q/%q", tl.ServerName(), tl.RemoteName())
	}

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"q":"hi","intent":"progress text"}`))
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if len(sess.calls) != 1 {
		t.Fatalf("expected 1 upstream call, got %d", len(sess.calls))
	}
	call := sess.calls[0]
	if call.name != "do" {
		t.Errorf("forwarded name = %q, want bare 'do'", call.name)
	}
	if _, hasIntent := call.args["intent"]; hasIntent {
		t.Error("intent must be stripped before forwarding to the server")
	}
	if call.args["q"] != "hi" {
		t.Errorf("arg q not forwarded: %#v", call.args)
	}
	if res.Metadata["mcp_server"] != "srv" || res.Metadata["mcp_tool"] != "do" {
		t.Errorf("missing origin metadata: %#v", res.Metadata)
	}
}

func TestRemoteToolExecuteReportsUpstreamErrorAsResult(t *testing.T) {
	sess := newFakeSession("do")
	sess.callErr = errors.New("transport down")
	tl := newRemoteTool("srv", RemoteToolSpec{Name: "do"}, sess, nil)

	res, err := tl.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute should not return a Go error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError result on upstream failure")
	}
}

func TestRemoteToolInputSchemaNeverNil(t *testing.T) {
	tl := newRemoteTool("srv", RemoteToolSpec{Name: "do"}, newFakeSession(), nil)
	sc := tl.InputSchema()
	if sc == nil || sc["type"] != "object" {
		t.Fatalf("expected non-nil object schema, got %#v", sc)
	}
}

func TestRemoteToolImplementsToolInterface(t *testing.T) {
	var _ tool.Tool = newRemoteTool("s", RemoteToolSpec{Name: "n"}, newFakeSession(), nil)
}
