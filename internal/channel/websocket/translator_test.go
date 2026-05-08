package websocket

import (
	"context"
	"reflect"
	"testing"

	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/pkg/types"
)

// makeRecorderEmitter wraps emit/v2 RecorderSink in an Emitter so tests
// can drive the translator and read the produced wire frames straight
// from a slice.
func makeRecorderEmitter(t *testing.T, sessionID string) (*emitv2.Emitter, *emitv2.RecorderSink) {
	t.Helper()
	rec := emitv2.NewRecorder()
	em := emitv2.New(emitv2.EmitterConfig{
		Sink:      rec,
		SessionID: sessionID,
		AgentID:   "main",
		AgentRole: emitv2.RolePersona,
	})
	return em, rec
}

// findClosePayload returns the ClosePayload of the first card.close
// targeting cardID, or fails the test if none.
func findClosePayload(t *testing.T, rec *emitv2.RecorderSink, cardID string) emitv2.ClosePayload {
	t.Helper()
	for _, ev := range rec.FilterByCard(cardID) {
		if ev.Type != emitv2.EventCardClose {
			continue
		}
		pl, ok := ev.Payload.(emitv2.ClosePayload)
		if !ok {
			t.Fatalf("close event payload type = %T", ev.Payload)
		}
		return pl
	}
	t.Fatalf("no card.close found for %s", cardID)
	return emitv2.ClosePayload{}
}

// TestTranslator_ToolEnd_PassesSearchMetadataThrough is the regression
// guard for the WebSearch / TavilySearch case: rich tool result
// metadata (urls, query, result_count, has_raw) must reach the wire
// via ToolPayload.Metadata, not be silently dropped.
func TestTranslator_ToolEnd_PassesSearchMetadataThrough(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_x")
	tr := NewTranslator()

	// Open a tool first so EngineEventToolEnd has a card to close.
	tr.Translate(em, "sess_x", &types.EngineEvent{
		Type:      types.EngineEventToolStart,
		ToolName:  "WebSearch",
		ToolUseID: "toolu_ws_1",
		ToolInput: `{"query":"vLLM 论文"}`,
	})

	tr.Translate(em, "sess_x", &types.EngineEvent{
		Type:      types.EngineEventToolEnd,
		ToolName:  "WebSearch",
		ToolUseID: "toolu_ws_1",
		ToolResult: &types.ToolResult{
			Content: "Search results for \"vLLM 论文\":\n\n--- Result 1 ---\nTitle: ...\nURL: https://...\n",
			Metadata: map[string]any{
				"render_hint":  "search",
				"query":        "vLLM 论文",
				"result_count": 5,
				"urls": []any{
					map[string]any{"url": "https://a.example", "title": "Result A"},
					map[string]any{"url": "https://b.example", "title": "Result B"},
				},
			},
		},
	})

	close := findClosePayload(t, rec, "toolu_ws_1")
	tp, ok := close.Inner.(emitv2.ToolPayload)
	if !ok {
		t.Fatalf("close.Inner is %T, want emitv2.ToolPayload", close.Inner)
	}

	// render_hint promoted to typed field.
	if tp.RenderHint != "search" {
		t.Errorf("RenderHint = %q, want search", tp.RenderHint)
	}
	// And stripped from passthrough Metadata (no duplication).
	if _, dup := tp.Metadata["render_hint"]; dup {
		t.Error("render_hint should be promoted out of Metadata, not duplicated")
	}
	// Search-specific fields preserved verbatim.
	if got, want := tp.Metadata["query"], "vLLM 论文"; got != want {
		t.Errorf("Metadata.query = %v, want %q (CRITICAL: regression of search metadata passthrough)", got, want)
	}
	if got, want := tp.Metadata["result_count"], 5; got != want {
		t.Errorf("Metadata.result_count = %v, want %d", got, want)
	}
	urls, ok := tp.Metadata["urls"].([]any)
	if !ok {
		t.Fatalf("Metadata.urls type = %T, want []any", tp.Metadata["urls"])
	}
	if len(urls) != 2 {
		t.Errorf("Metadata.urls len = %d, want 2", len(urls))
	}
}

// TestTranslator_ToolEnd_PassesTavilyHasRawThrough is a smaller
// counterpart for the tavily-specific has_raw flag.
func TestTranslator_ToolEnd_PassesTavilyHasRawThrough(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_t")
	tr := NewTranslator()
	tr.Translate(em, "sess_t", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "TavilySearch", ToolUseID: "toolu_tv",
		ToolInput: `{"query":"x"}`,
	})
	tr.Translate(em, "sess_t", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "TavilySearch", ToolUseID: "toolu_tv",
		ToolResult: &types.ToolResult{
			Content: "...",
			Metadata: map[string]any{
				"render_hint": "search",
				"has_raw":     true,
				"query":       "x",
			},
		},
	})
	tp := findClosePayload(t, rec, "toolu_tv").Inner.(emitv2.ToolPayload)
	if tp.Metadata["has_raw"] != true {
		t.Errorf("has_raw lost: got %v", tp.Metadata["has_raw"])
	}
}

// TestTranslator_ToolEnd_PromotesAllKnownKeys verifies every promoted
// field gets stripped from Metadata so the wire never duplicates
// known keys.
func TestTranslator_ToolEnd_PromotesAllKnownKeys(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_p")
	tr := NewTranslator()
	tr.Translate(em, "sess_p", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "Bash", ToolUseID: "toolu_p",
		ToolInput: `{}`,
	})
	tr.Translate(em, "sess_p", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "Bash", ToolUseID: "toolu_p",
		ToolResult: &types.ToolResult{
			Content: "out",
			Metadata: map[string]any{
				"render_hint": "terminal",
				"language":    "bash",
				"file_path":   "/tmp/x.sh",
				"exit_code":   0,
			},
		},
	})
	tp := findClosePayload(t, rec, "toolu_p").Inner.(emitv2.ToolPayload)
	if tp.RenderHint != "terminal" || tp.Language != "bash" || tp.FilePath != "/tmp/x.sh" {
		t.Errorf("typed promotion failed: %+v", tp)
	}
	want := map[string]any{"exit_code": 0}
	if !reflect.DeepEqual(tp.Metadata, want) {
		t.Errorf("Metadata after promotion = %+v, want %+v (only non-promoted keys remain)", tp.Metadata, want)
	}
}

// TestTranslator_ToolEnd_NoMetadataNoMap verifies that when a tool
// returns nil Metadata, ToolPayload.Metadata stays nil (so the wire
// frame omits the field rather than carrying an empty object).
func TestTranslator_ToolEnd_NoMetadataNoMap(t *testing.T) {
	em, rec := makeRecorderEmitter(t, "sess_n")
	tr := NewTranslator()
	tr.Translate(em, "sess_n", &types.EngineEvent{
		Type: types.EngineEventToolStart, ToolName: "X", ToolUseID: "tu_n",
	})
	tr.Translate(em, "sess_n", &types.EngineEvent{
		Type: types.EngineEventToolEnd, ToolName: "X", ToolUseID: "tu_n",
		ToolResult: &types.ToolResult{Content: "ok"},
	})
	tp := findClosePayload(t, rec, "tu_n").Inner.(emitv2.ToolPayload)
	if tp.Metadata != nil {
		t.Errorf("Metadata should be nil when tool has none, got %+v", tp.Metadata)
	}
}

// TestPromoteToolMetadata_OmitsKnownKeys exercises the helper directly.
func TestPromoteToolMetadata_OmitsKnownKeys(t *testing.T) {
	rh, lang, fp, rest := promoteToolMetadata(map[string]any{
		"render_hint": "search",
		"language":    "go",
		"file_path":   "/a",
		"extra":       42,
	})
	if rh != "search" || lang != "go" || fp != "/a" {
		t.Errorf("typed: rh=%q lang=%q fp=%q", rh, lang, fp)
	}
	if !reflect.DeepEqual(rest, map[string]any{"extra": 42}) {
		t.Errorf("rest = %+v", rest)
	}
}

func TestPromoteToolMetadata_NilWhenEmpty(t *testing.T) {
	if _, _, _, rest := promoteToolMetadata(nil); rest != nil {
		t.Errorf("nil input should yield nil rest, got %+v", rest)
	}
	if _, _, _, rest := promoteToolMetadata(map[string]any{
		"render_hint": "search",
	}); rest != nil {
		t.Errorf("only-known-keys input should yield nil rest, got %+v", rest)
	}
}

// silence unused import when no wait references; needed to keep parity
// with the existing translator package layout.
var _ = context.Background
