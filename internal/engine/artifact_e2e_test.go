package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/artifact"
	"harnessclaw-go/internal/emit"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/artifacttool"
	"harnessclaw-go/pkg/types"
)

// TestArtifactsE2E_TwoAgentHandoff drives the full §6 mode-A loop in one
// process:
//
//	(1) Agent A is spawned with a scripted ArtifactWrite call.
//	(2) Agent B is spawned in the SAME trace; the framework auto-injects
//	    the available-artifacts preamble carrying A's ID into B's prompt.
//	(3) Agent B's first turn calls ArtifactRead(mode=full) on that ID.
//	(4) The store returns the original content; B receives it via tool
//	    result; emits subagent_end carrying NO new artifacts (read != write).
//
// If any link breaks — preamble missing the ID, read failing to dereference,
// SubAgentEvent forwarding dropping the Ref — the assertions below catch it.
// Without this test, the four moving pieces (executor, SpawnSync forwarder,
// preamble injector, store) could each pass their unit tests yet still be
// wired wrong end-to-end.
func TestArtifactsE2E_TwoAgentHandoff(t *testing.T) {
	const (
		traceID  = "tr_e2e_handoff"
		artName  = "input-data.md"
		artDesc  = "agent A's findings"
		artBody  = "<finding>\n2024 Q4 revenue up 20%\n</finding>"
	)

	store := artifact.NewMemoryStore(artifact.DefaultConfig())

	// ---- Round 1: Agent A writes an artifact ----
	provA := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				toolCalls: []types.ToolCall{{
					ID:   "tu_write",
					Name: artifacttool.WriteToolName,
					Input: writeInput(artName, artDesc, artBody),
				}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
			{
				text:       "<summary>findings stored</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
	}
	engA := newSubagentTestEngine(provA, artifacttool.NewWriteTool())
	engA.SetArtifactStore(store)

	ctx := emit.WithTrace(context.Background(), &emit.TraceContext{
		TraceID:   traceID,
		Sequencer: emit.NewSequencer(),
	})

	resA, err := engA.SpawnSync(ctx, &agent.SpawnConfig{
		Prompt:          "store the findings",
		AgentType:       tool.AgentTypeSync,
		ParentSessionID: "p_a",
	})
	if err != nil {
		t.Fatalf("agent A SpawnSync: %v", err)
	}
	_ = resA

	// Recover the actual ID the store assigned. We can't predict it ahead
	// of time (NewID is server-side random — that's the whole point), so
	// list what's in the trace and grab the only entry.
	stored, err := store.List(ctx, &artifact.ListFilter{TraceID: traceID})
	if err != nil || len(stored) != 1 {
		t.Fatalf("expected 1 artifact in trace after agent A; got %d err=%v", len(stored), err)
	}
	artID := stored[0].ID
	if artID == "" {
		t.Fatal("store returned empty artifact_id")
	}

	// ---- Round 2: Agent B reads it ----
	// Note Input must use the runtime-discovered artID — we can only know
	// it AFTER A ran. Real LLMs read the preamble to learn the ID; here
	// we simulate that read by building the input with the ID we just got.
	provB := &subagentMockProvider{
		responses: []subagentMockResponse{
			{
				toolCalls: []types.ToolCall{{
					ID:   "tu_read",
					Name: artifacttool.ReadToolName,
					Input: readInput(artID, "full"),
				}},
				stopReason: "tool_use",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
			{
				text:       "<summary>read complete</summary>",
				stopReason: "end_turn",
				usage:      &types.Usage{InputTokens: 1, OutputTokens: 1},
			},
		},
	}
	engB := newSubagentTestEngine(provB, artifacttool.NewReadTool())
	engB.SetArtifactStore(store)

	parentOut := make(chan types.EngineEvent, 32)
	doneB := make(chan error, 1)
	go func() {
		_, err := engB.SpawnSync(ctx, &agent.SpawnConfig{
			Prompt:          "整理 agent A 的发现",
			AgentType:       tool.AgentTypeSync,
			ParentSessionID: "p_b",
			ParentOut:       parentOut,
		})
		close(parentOut)
		doneB <- err
	}()

	// Drain B's events into something we can assert on.
	var (
		readToolEnd    *types.SubAgentEventData
		bSubAgentEnd   *types.EngineEvent
	)
	for evt := range parentOut {
		evt := evt
		switch evt.Type {
		case types.EngineEventSubAgentEvent:
			if evt.SubAgentEvent != nil &&
				evt.SubAgentEvent.EventType == "tool_end" &&
				evt.SubAgentEvent.ToolName == artifacttool.ReadToolName {
				readToolEnd = evt.SubAgentEvent
			}
		case types.EngineEventSubAgentEnd:
			bSubAgentEnd = &evt
		}
	}
	if err := <-doneB; err != nil {
		t.Fatalf("agent B SpawnSync: %v", err)
	}

	// ---- Assertion #1: preamble carried A's artifact into B's prompt ----
	// Agent B's mock provider recorded the system + messages it received.
	// The first user turn must contain a <available-artifacts> block with
	// A's ID — otherwise the framework didn't inject the §6.A preamble
	// and a real LLM would never know there was data to read.
	bUserText := provB.lastUserText()
	for _, want := range []string{"<available-artifacts>", artID, artName, artDesc} {
		if !strings.Contains(bUserText, want) {
			t.Errorf("agent B's prompt missing %q\n--- prompt ---\n%s", want, bUserText)
		}
	}

	// ---- Assertion #2: ArtifactRead returned the original content ----
	if readToolEnd == nil {
		t.Fatal("agent B never emitted a tool_end for ArtifactRead — read path broken")
	}
	if readToolEnd.IsError {
		t.Fatalf("ArtifactRead returned error: %s", readToolEnd.Output)
	}
	// The tool output is JSON; deserialise to verify the artifact body
	// round-tripped intact.
	var readResp struct {
		Mode     string `json:"mode"`
		Artifact struct {
			ID      string `json:"artifact_id"`
			Content string `json:"content"`
			Name    string `json:"name"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal([]byte(readToolEnd.Output), &readResp); err != nil {
		t.Fatalf("read response JSON unparseable: %v\n%s", err, readToolEnd.Output)
	}
	if readResp.Mode != "full" {
		t.Errorf("read mode = %q, want full", readResp.Mode)
	}
	if readResp.Artifact.ID != artID {
		t.Errorf("read returned wrong ID: %q vs %q", readResp.Artifact.ID, artID)
	}
	if readResp.Artifact.Content != artBody {
		t.Errorf("read content corrupted:\n  got  %q\n  want %q", readResp.Artifact.Content, artBody)
	}

	// ---- Assertion #3: B's subagent_end carries no NEW artifacts ----
	// Reading a parent's artifact must not surface it again on B's
	// produced-artifacts list — only WRITES count as production. Without
	// this guard the UI would double-count: A's card, then B's "produced"
	// card containing the same artifact.
	if bSubAgentEnd == nil {
		t.Fatal("agent B never emitted subagent_end")
	}
	if len(bSubAgentEnd.Artifacts) != 0 {
		t.Errorf("agent B subagent_end.Artifacts should be empty (B only read); got %d:\n  %+v",
			len(bSubAgentEnd.Artifacts), bSubAgentEnd.Artifacts)
	}

	// ---- Assertion #4: store still has exactly one artifact ----
	// Read is non-mutating; read with mode=full must not bump the version
	// counter or otherwise touch the record.
	post, _ := store.List(ctx, &artifact.ListFilter{TraceID: traceID})
	if len(post) != 1 {
		t.Errorf("store leaked artifacts during read: pre=1, post=%d", len(post))
	}
	if post[0].Version != 1 {
		t.Errorf("read mutated version: %d (want 1)", post[0].Version)
	}
}

// writeInput renders the JSON ArtifactWrite expects. Kept terse since the
// quoting gets noisy when interleaved with other test logic.
func writeInput(name, desc, content string) string {
	body, _ := json.Marshal(map[string]any{
		"intent":      "store fixture",
		"type":        "file",
		"name":        name,
		"description": desc,
		"mime_type":   "text/markdown",
		"content":     content,
	})
	return string(body)
}

// readInput renders the JSON ArtifactRead expects.
func readInput(id, mode string) string {
	body, _ := json.Marshal(map[string]any{
		"intent":      "fetch fixture",
		"artifact_id": id,
		"mode":        mode,
	})
	return string(body)
}
