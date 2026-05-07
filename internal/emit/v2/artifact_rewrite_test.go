package emitv2

import (
	"strings"
	"testing"
)

func TestArtifactRegistry_RecordAndLookup(t *testing.T) {
	r := NewArtifactRegistry()
	r.Record("foo.md", "art_a1")
	id, ok := r.Lookup("foo.md")
	if !ok || id != "art_a1" {
		t.Errorf("Lookup = %q, %v", id, ok)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Error("missing name should not be found")
	}
}

func TestArtifactRegistry_RecordRefs(t *testing.T) {
	r := NewArtifactRegistry()
	r.RecordRefs([]ArtifactRef{
		{ArtifactID: "art_a", Name: "a.md"},
		{ArtifactID: "art_b", Name: "b.md"},
	})
	if id, _ := r.Lookup("a.md"); id != "art_a" {
		t.Errorf("a.md = %q", id)
	}
	if id, _ := r.Lookup("b.md"); id != "art_b" {
		t.Errorf("b.md = %q", id)
	}
}

func TestArtifactRegistry_Rewrite_Basic(t *testing.T) {
	r := NewArtifactRegistry()
	r.Record("intern-schedule-email.md", "art_a1b2")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"raw mention is rewritten",
			"邮件已经准备好：intern-schedule-email.md，正式商务语气",
			"邮件已经准备好：[intern-schedule-email.md](artifact://art_a1b2)，正式商务语气",
		},
		{
			"already-formatted markdown URI stays untouched",
			"看 [intern-schedule-email.md](artifact://art_a1b2) 这份",
			"看 [intern-schedule-email.md](artifact://art_a1b2) 这份",
		},
		{
			"empty text is no-op",
			"",
			"",
		},
		{
			"unrelated text untouched",
			"今天天气真好",
			"今天天气真好",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Rewrite(c.in)
			if got != c.want {
				t.Errorf("got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestArtifactRegistry_Rewrite_LongestFirst(t *testing.T) {
	// "report.md" must not match inside "annual-report.md".
	r := NewArtifactRegistry()
	r.Record("report.md", "art_short")
	r.Record("annual-report.md", "art_long")

	got := r.Rewrite("发了 annual-report.md 给你")
	if !strings.Contains(got, "[annual-report.md](artifact://art_long)") {
		t.Errorf("longest-first match failed: %q", got)
	}
	// "report.md" should NOT match because it's part of "annual-report.md"
	// — the boundary check guards word characters around the match.
	if strings.Count(got, "artifact://") != 1 {
		t.Errorf("expected exactly 1 rewrite, got: %q", got)
	}
}

func TestArtifactRegistry_Rewrite_WordBoundary(t *testing.T) {
	r := NewArtifactRegistry()
	r.Record("foo", "art_x")

	// "foo" inside "foobar" should NOT be rewritten.
	in := "look at foobar.txt"
	got := r.Rewrite(in)
	if got != in {
		t.Errorf("foo should not match inside foobar: %q", got)
	}

	// "foo" with whitespace boundary SHOULD be rewritten.
	got2 := r.Rewrite("look at foo here")
	if !strings.Contains(got2, "[foo](artifact://art_x)") {
		t.Errorf("foo at boundary should rewrite: %q", got2)
	}
}

func TestArtifactRegistry_Rewrite_MultipleOccurrences(t *testing.T) {
	r := NewArtifactRegistry()
	r.Record("a.md", "art_a")

	got := r.Rewrite("先看 a.md，再看 a.md，最后 a.md")
	if strings.Count(got, "[a.md](artifact://art_a)") != 3 {
		t.Errorf("expected 3 rewrites, got: %q", got)
	}
}

func TestArtifactRegistry_Rewrite_EmptyRegistryNoOp(t *testing.T) {
	r := NewArtifactRegistry()
	in := "hello world"
	got := r.Rewrite(in)
	if got != in {
		t.Errorf("empty registry should be no-op, got %q", got)
	}
}

// Builder integration: text appends on message cards auto-rewrite when
// an earlier event introduced a matching artifact.
func TestBuilder_AutoRewriteArtifactInMessage(t *testing.T) {
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink:      rec,
		SessionID: "s",
		TraceID:   "tr",
		Artifacts: NewArtifactRegistry(),
	})

	// Sub-agent produces an artifact (close event carries the ref).
	em.Card(CardTurn, "turn_1").Add(TurnPayload{TurnNo: 1})
	em.Card(CardTool, "tool_1").Add(ToolPayload{Name: "ArtifactWrite"}, WithParent("turn_1"))
	em.Card(CardTool, "tool_1").Close(StatusOK, WithInner(ToolPayload{
		Artifacts: []ArtifactRef{{ArtifactID: "art_a1b2", Name: "report.md"}},
	}))

	// emma later writes a text chunk that mentions the artifact.
	em.Card(CardMessage, "msg_1").Add(MessagePayload{}, WithParent("turn_1"))
	em.Card(CardMessage, "msg_1").Append(ChannelText, "看 report.md 就好")
	em.Card(CardMessage, "msg_1").Close(StatusOK)
	em.Card(CardTurn, "turn_1").Close(StatusOK)

	appends := rec.FilterByType(EventCardAppend)
	if len(appends) != 1 {
		t.Fatalf("got %d appends", len(appends))
	}
	pl := appends[0].Payload.(AppendPayload)
	if !strings.Contains(pl.Chunk, "[report.md](artifact://art_a1b2)") {
		t.Errorf("expected artifact:// URI rewrite, got: %q", pl.Chunk)
	}
}

// Tool input chunks are NOT rewritten (rewrite is text-only).
func TestBuilder_NoRewriteOnToolInput(t *testing.T) {
	rec := NewRecorder()
	em := New(EmitterConfig{
		Sink:      rec,
		SessionID: "s",
		TraceID:   "tr",
		Artifacts: NewArtifactRegistry(),
	})

	em.artifacts.Record("report.md", "art_x")
	em.Card(CardMessage, "msg_1").Add(MessagePayload{})
	em.Card(CardMessage, "msg_1").Append(ChannelToolInput, `{"file":"report.md"}`)

	appends := rec.FilterByType(EventCardAppend)
	pl := appends[0].Payload.(AppendPayload)
	if !strings.Contains(pl.PartialJSON, "report.md") || strings.Contains(pl.PartialJSON, "artifact://") {
		t.Errorf("tool_input should NOT be rewritten, got: %q", pl.PartialJSON)
	}
}

// Without an Artifacts registry on the Emitter, no rewrite happens.
func TestBuilder_NoArtifactRegistryNoRewrite(t *testing.T) {
	rec := NewRecorder()
	em := New(EmitterConfig{Sink: rec, SessionID: "s"})
	em.Card(CardMessage, "msg_1").Add(MessagePayload{})
	em.Card(CardMessage, "msg_1").Append(ChannelText, "report.md")

	pl := rec.FilterByType(EventCardAppend)[0].Payload.(AppendPayload)
	if pl.Chunk != "report.md" {
		t.Errorf("without registry, text should pass through; got %q", pl.Chunk)
	}
}
