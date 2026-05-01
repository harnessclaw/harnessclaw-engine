package websearch

import (
	"strings"
	"testing"
)

// TestExtractResults_FromArrayPicksKnownKeys verifies the parser recognises
// the field aliases iFly's response uses — without this, the format-flip
// from "Content" to "Summary" silently drops data.
func TestExtractResults_FromArrayPicksKnownKeys(t *testing.T) {
	raw := []byte(`{
		"code": 0,
		"data": {
			"results": [
				{"title": "Go 1.22 release notes", "url": "https://go.dev/doc/go1.22",
					"snippet": "Go 1.22 enhances loop variable scoping and introduces ranges over integers."},
				{"name": "Range over int (proposal)", "link": "https://github.com/golang/go/issues/61405",
					"abstract": "A proposal to allow ranging over integer values in Go 1.22."}
			]
		}
	}`)
	got, err := extractResults(raw)
	if err != nil {
		t.Fatalf("extractResults: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].Title == "" || got[0].URL == "" || got[0].Snippet == "" {
		t.Errorf("result 0 missing fields: %+v", got[0])
	}
	if got[1].Title == "" || got[1].URL == "" || got[1].Snippet == "" {
		t.Errorf("result 1 missing fields (alias keys): %+v", got[1])
	}
}

// formatResultsForLLM is the smallest reproduction of the LLM-facing
// output Execute() builds. We extract the formatting separately so a
// test can lock it down without spinning up an HTTP server. Keep this in
// sync with the loop in Execute() — if you change one, change both.
func formatResultsForLLM(query string, results []searchResult) string {
	var sb strings.Builder
	sb.WriteString("Search results for \"" + query + "\":\n\n")
	for i, r := range results {
		sb.WriteString("--- Result ")
		sb.WriteString(itoa(i + 1))
		sb.WriteString(" ---\n")
		sb.WriteString("Title: " + r.Title + "\n")
		sb.WriteString("URL: " + r.URL + "\n")
		summary := r.Snippet
		if summary == "" && r.FullText != "" {
			summary = truncate(r.FullText, MaxSummaryChars)
		}
		if summary != "" {
			sb.WriteString("Summary:\n" + summary + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("---\n")
	sb.WriteString("Note: only summaries are shown above. If a result looks relevant ")
	sb.WriteString("but the summary is not enough to answer, call the WebFetch tool ")
	sb.WriteString("with that URL to retrieve the full page content.\n")
	return sb.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestFormat_PrefersSnippetOverFullText(t *testing.T) {
	out := formatResultsForLLM("Go 1.22", []searchResult{
		{
			Title:    "Go 1.22 release notes",
			URL:      "https://go.dev/doc/go1.22",
			Snippet:  "Short curated summary from the search API.",
			FullText: strings.Repeat("LARGE FULL PAGE BODY ", 500), // ~10 KB
		},
	})

	if !strings.Contains(out, "Summary:\nShort curated summary") {
		t.Errorf("expected snippet to be used; got:\n%s", out)
	}
	if strings.Contains(out, "LARGE FULL PAGE BODY") {
		t.Errorf("FullText leaked into output despite Snippet being present")
	}
	if strings.Contains(out, "Content:") {
		t.Errorf("legacy 'Content:' label still present")
	}
}

func TestFormat_FallsBackToTruncatedFullTextWhenNoSnippet(t *testing.T) {
	out := formatResultsForLLM("query", []searchResult{
		{
			Title:    "page",
			URL:      "https://example.com",
			FullText: strings.Repeat("x", MaxSummaryChars*4), // way over the cap
		},
	})

	idx := strings.Index(out, "Summary:\n")
	if idx == -1 {
		t.Fatalf("expected Summary section; got:\n%s", out)
	}
	// Output between "Summary:\n" and the next "\n\n" should be ≤ MaxSummaryChars
	// + "..." suffix.
	body := out[idx+len("Summary:\n"):]
	end := strings.Index(body, "\n\n")
	if end == -1 {
		t.Fatalf("malformed output: %s", body)
	}
	body = body[:end]
	// Strip trailing newline before the blank line.
	body = strings.TrimRight(body, "\n")
	if len(body) > MaxSummaryChars+len("...") {
		t.Errorf("FullText fallback not truncated: len=%d, cap=%d", len(body), MaxSummaryChars)
	}
}

func TestFormat_IncludesTwoStageHint(t *testing.T) {
	out := formatResultsForLLM("anything", []searchResult{
		{Title: "t", URL: "https://x", Snippet: "s"},
	})
	// The two-stage hint is what trains the LLM to reach for WebFetch when
	// the summary is too thin. Without it the LLM tends to answer from
	// summary alone — losing the whole point of the two-stage design.
	if !strings.Contains(out, "WebFetch") {
		t.Errorf("missing WebFetch hint in footer; LLM won't know to follow up. Got:\n%s", out)
	}
	if !strings.Contains(out, "only summaries are shown above") {
		t.Errorf("missing summary-only disclaimer; got:\n%s", out)
	}
}

func TestFormat_StaysUnderArtifactThreshold(t *testing.T) {
	// 5 typical-sized results × 250-char summary should comfortably fit
	// under the 4 KB artifact threshold so the LLM sees ALL summaries
	// without truncation. If this test fails after a future change, the
	// summary-first design has regressed and the LLM will start seeing
	// "[truncated, full content persisted as artifact ...]" instead of
	// the actual results.
	const artifactThreshold = 4096
	results := make([]searchResult, 5)
	for i := range results {
		results[i] = searchResult{
			Title:   "Result title number " + itoa(i),
			URL:     "https://example.com/page" + itoa(i),
			Snippet: strings.Repeat("Some curated summary text. ", 10), // ~270 chars
		}
	}
	out := formatResultsForLLM("typical query", results)
	if len(out) >= artifactThreshold {
		t.Errorf("formatted output is %d bytes, exceeds %d artifact threshold — "+
			"the LLM will see a truncated preview instead of full result list",
			len(out), artifactThreshold)
	}
}
