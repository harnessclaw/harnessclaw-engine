package spawn_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/queryloop"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// newSpawnTestEngine constructs a full *engine.QueryEngine via the public
// constructor. Pass a non-nil defReg to enable @-mention routing; the
// engine wires both qe.defRegistry and qe.mentionParser from the Config
// (no backdoor writes required).
func newSpawnTestEngine(
	t *testing.T,
	prov provider.Provider,
	defReg *agent.AgentDefinitionRegistry,
	tools ...tool.Tool,
) *engine.QueryEngine {
	t.Helper()
	return newSpawnTestEngineWithName(t, prov, defReg, "", tools...)
}

// newSpawnTestEngineWithName is the displayName-aware variant used by
// tests that exercise the worker-identity prompt path. The legacy
// engine-package tests poked qe.config.MainAgentDisplayName directly
// after construction; the migrated tests pass it via QueryEngineConfig
// instead.
func newSpawnTestEngineWithName(
	t *testing.T,
	prov provider.Provider,
	defReg *agent.AgentDefinitionRegistry,
	displayName string,
	tools ...tool.Tool,
) *engine.QueryEngine {
	t.Helper()
	logger := zap.NewNop()
	store := memory.New()
	bus := event.NewBus()
	mgr := session.NewManager(store, logger, 30*time.Minute)
	cmdReg := command.NewRegistry()

	reg := tool.NewRegistry()
	for _, tl := range tools {
		_ = reg.Register(tl)
	}

	cfg := engine.QueryEngineConfig{
		MaxTurns:             50,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          30 * time.Second,
		MaxTokens:            4096,
		SystemPrompt:         "You are a test assistant.",
		ClientTools:          false,
		// Tests don't have a client to answer the failure-decision
		// prompt; without this flag, any failure path would block
		// SpawnSync forever waiting for a SubmitStepDecision that never
		// comes. Production keeps the gate enabled (the default).
		DisableStepDecisionGate: true,
		// DefRegistry, when set, wires both qe.defRegistry and the
		// mention parser inside NewQueryEngine — the public path that
		// replaces the old backdoor writes used by the engine-package
		// version of these tests.
		DefRegistry:          defReg,
		MainAgentDisplayName: displayName,
	}

	return engine.NewQueryEngine(prov, reg, mgr, nil, permission.BypassChecker{}, bus, logger, cfg, cmdReg)
}

// --- Mock provider for sub-agent tests (lifted from the old engine package
// subagent_test.go; kept here so all four migrated test files share it). ---

type subagentMockProvider struct {
	responses []subagentMockResponse
	callIdx   int
	recorded  []recordedReq
	// responseFn, when set, overrides `responses` — gives the test
	// just-in-time control over each response (e.g. constructing turn-N
	// tool inputs from store state created by turn-(N-1)).
	responseFn func(callIdx int) subagentMockResponse
}

type subagentMockResponse struct {
	text       string
	toolCalls  []types.ToolCall
	stopReason string
	usage      *types.Usage
	err        error
}

func (m *subagentMockProvider) Name() string { return "mock-subagent" }

// recordedReqs captures every Chat() request the engine made — opt-in via
// the new SpawnSync_PreambleInjection test, harmless to existing callers.
type recordedReq struct {
	System   string
	Messages []types.Message
}

func (m *subagentMockProvider) lastUserText() string {
	if len(m.recorded) == 0 {
		return ""
	}
	last := m.recorded[len(m.recorded)-1]
	for _, msg := range last.Messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, cb := range msg.Content {
			if cb.Type == types.ContentTypeText {
				return cb.Text
			}
		}
	}
	return ""
}

func (m *subagentMockProvider) Chat(_ context.Context, req *provider.ChatRequest) (*provider.ChatStream, error) {
	if req != nil {
		m.recorded = append(m.recorded, recordedReq{
			System:   req.System,
			Messages: append([]types.Message(nil), req.Messages...),
		})
	}
	if m.responseFn != nil {
		resp := m.responseFn(m.callIdx)
		m.callIdx++
		if resp.err != nil {
			return nil, resp.err
		}
		return newSubagentMockStream(resp.text, resp.toolCalls, resp.stopReason, resp.usage), nil
	}
	if m.callIdx >= len(m.responses) {
		stream := newSubagentMockStream("", nil, "end_turn", &types.Usage{InputTokens: 10, OutputTokens: 5})
		return stream, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	if resp.err != nil {
		return nil, resp.err
	}
	return newSubagentMockStream(resp.text, resp.toolCalls, resp.stopReason, resp.usage), nil
}

func (m *subagentMockProvider) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 100, nil
}

func newSubagentMockStream(text string, toolCalls []types.ToolCall, stopReason string, usage *types.Usage) *provider.ChatStream {
	ch := make(chan types.StreamEvent, 10)

	go func() {
		defer close(ch)
		if text != "" {
			ch <- types.StreamEvent{Type: types.StreamEventText, Text: text}
		}
		for _, tc := range toolCalls {
			tc := tc
			ch <- types.StreamEvent{Type: types.StreamEventToolUse, ToolCall: &tc}
		}
		ch <- types.StreamEvent{
			Type:       types.StreamEventMessageEnd,
			StopReason: stopReason,
			Usage:      usage,
		}
	}()

	return &provider.ChatStream{
		Events: ch,
		Err:    func() error { return nil },
	}
}

// engineFakeProv is the dependency-light Provider used by the token
// attribution tests. It replays a fixed StreamEvent slice and reports a
// stable Name. Lifted verbatim (with rename) from the old engine package
// test_helpers_test.go so these tests can live external to the engine.
type engineFakeProv struct {
	events []types.StreamEvent
	err    error
}

func (f *engineFakeProv) Chat(_ context.Context, _ *provider.ChatRequest) (*provider.ChatStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan types.StreamEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return &provider.ChatStream{Events: ch, Err: func() error { return nil }}, nil
}

func (f *engineFakeProv) CountTokens(_ context.Context, _ []types.Message) (int, error) {
	return 0, nil
}

func (f *engineFakeProv) Name() string { return "engineFakeProv" }

// containsStr / indexOfStr are tiny helpers shared by the migrated tests;
// kept local so the package doesn't need to depend on strings.Contains
// idioms across multiple files.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// silence unused-import warning if a future edit drops the queryloop
// reference from this file (the helper exists to make the dep explicit).
var _ = queryloop.NewMentionParser
