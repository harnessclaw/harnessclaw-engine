// Package websearch implements the WebSearch tool using the iFly search API.
package websearch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	toolName     = "WebSearch"
	fetchTimeout = 30 * time.Second
)

// searchInput is the JSON structure the LLM sends to invoke the tool.
type searchInput struct {
	Query string `json:"query"`
}

// WebSearchTool performs web searches via the iFly search API.
type WebSearchTool struct {
	tool.BaseTool
	cfg    config.WebSearchConfig
	client *http.Client
	logger *zap.Logger
}

// New creates a WebSearchTool with the given config.
func New(cfg config.WebSearchConfig, logger *zap.Logger) *WebSearchTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &WebSearchTool{
		cfg: cfg,
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		logger: logger.Named("websearch"),
	}
}

func (t *WebSearchTool) Name() string            { return toolName }
func (t *WebSearchTool) Description() string     { return webSearchDescription }
func (t *WebSearchTool) IsReadOnly() bool                  { return true }
func (t *WebSearchTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *WebSearchTool) IsConcurrencySafe() bool  { return true }

func (t *WebSearchTool) IsEnabled() bool {
	return t.cfg.Enabled && t.cfg.APIKey != "" && t.cfg.APISecret != "" && t.cfg.AppID != ""
}

func (t *WebSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "搜索 query。",
				"minLength":   2,
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) ValidateInput(input json.RawMessage) error {
	var si searchInput
	if err := json.Unmarshal(input, &si); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(si.Query) == "" {
		return fmt.Errorf("query is required")
	}
	return nil
}

func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var si searchInput
	if err := json.Unmarshal(input, &si); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	query := strings.TrimSpace(si.Query)
	t.logger.Info("web search", zap.String("query", query))

	start := time.Now()

	// Build signed URL.
	signedURL := t.buildSignedURL()

	// Build request body.
	limit := t.cfg.Limit
	if limit <= 0 {
		limit = 5
	}
	body := map[string]any{
		"disable_crawler":    false,
		"pipeline_name":      "pl_map_agg_search_biz",
		"sid":                "",
		"uId":                "",
		"appId":              t.cfg.AppID,
		"limit":              limit,
		"business":           "bot1.0",
		"name":               query,
		"disable_highlight":  true,
		"open_rerank":        true,
		"full_text":          false,
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return &types.ToolResult{Content: "error encoding request: " + err.Error(), IsError: true}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, signedURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return &types.ToolResult{Content: "error creating request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.logger.Warn("web search request failed",
			zap.String("query", query),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "search request failed: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &types.ToolResult{Content: "error reading response: " + err.Error(), IsError: true}, nil
	}

	if resp.StatusCode != http.StatusOK {
		t.logger.Warn("web search HTTP error",
			zap.String("query", query),
			zap.Int("status", resp.StatusCode),
			zap.String("response", string(respBody)),
			zap.Duration("elapsed", elapsed),
		)
		return &types.ToolResult{
			Content: fmt.Sprintf("search API returned HTTP %d", resp.StatusCode),
			IsError: true,
		}, nil
	}
	if err != nil {
		return &types.ToolResult{Content: "error reading response: " + err.Error(), IsError: true}, nil
	}

	// Parse the response and extract search results.
	t.logger.Debug("web search raw response",
		zap.String("query", query),
		zap.Int("body_len", len(respBody)),
		zap.String("body", truncate(string(respBody), 2000)),
	)

	results, err := extractResults(respBody)
	if err != nil {
		t.logger.Warn("web search parse error",
			zap.String("query", query),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "error parsing search results: " + err.Error(), IsError: true}, nil
	}

	t.logger.Info("web search success",
		zap.String("query", query),
		zap.Int("result_count", len(results)),
		zap.Duration("elapsed", elapsed),
	)

	if len(results) == 0 {
		return &types.ToolResult{Content: "No results found for: " + query}, nil
	}

	// Two-stage retrieval design (since 2026-04):
	//   stage 1 (this tool)  → return only Title + URL + Summary per result
	//   stage 2 (LLM choice) → if a summary looks promising, the LLM calls
	//                          WebFetch on that URL to get the full page
	//
	// Why summary-only:
	//   - Cuts injected context by ~10× (typical: 5 results × 200-char
	//     summary = ~1.5 KB, vs. 5 × 3 KB full text = 15 KB)
	//   - Lets the LLM be selective — only fetch what's actually useful
	//     instead of paying for everything upfront
	//
	// Build Content for LLM: title + URL + summary per result, plus a
	// trailing hint about the WebFetch follow-up.
	// Build Metadata for the WebSocket client: URLs for display.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for %q:\n\n", query))

	type urlEntry struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	var urlEntries []urlEntry

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("--- Result %d ---\n", i+1))
		sb.WriteString(fmt.Sprintf("Title: %s\n", r.Title))
		sb.WriteString(fmt.Sprintf("URL: %s\n", r.URL))

		// Summary preference order:
		//   1. Snippet from the search API (preferred — already curated)
		//   2. First MaxSummaryChars of FullText (defensive fallback when
		//      the API only returned full text and no abstract)
		summary := r.Snippet
		if summary == "" && r.FullText != "" {
			summary = truncate(r.FullText, MaxSummaryChars)
		}
		if summary != "" {
			sb.WriteString(fmt.Sprintf("Summary:\n%s\n", summary))
		}
		sb.WriteString("\n")

		urlEntries = append(urlEntries, urlEntry{URL: r.URL, Title: r.Title})
	}

	// Footer: explicitly cue the next step. Without this prompt the LLM
	// often answers from summaries alone even when the user clearly needs
	// detail — it doesn't know fetching is cheap and on-policy.
	sb.WriteString("---\n")
	sb.WriteString("Note: only summaries are shown above. If a result looks relevant ")
	sb.WriteString("but the summary is not enough to answer, call the WebFetch tool ")
	sb.WriteString("with that URL to retrieve the full page content.\n")

	return &types.ToolResult{
		Content: sb.String(),
		Metadata: map[string]any{
			"render_hint":  "search",
			"query":        query,
			"result_count": len(results),
			"urls":         urlEntries,
		},
	}, nil
}

// MaxSummaryChars caps the per-result summary length. Snippets from the
// iFly search API are usually 100-300 chars and won't be touched; the cap
// only fires on the defensive FullText fallback when no snippet exists.
const MaxSummaryChars = 500

// buildSignedURL constructs the authorization-signed URL for the iFly search API.
func (t *WebSearchTool) buildSignedURL() string {
	host := t.cfg.Host
	path := t.cfg.Path

	// RFC 1123 UTC time.
	date := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")

	// Signature: HMAC-SHA256(api_secret, "host: {host}\ndate: {date}\nPOST {path} HTTP/1.1")
	signatureOrigin := fmt.Sprintf("host: %s\ndate: %s\nPOST %s HTTP/1.1", host, date, path)
	mac := hmac.New(sha256.New, []byte(t.cfg.APISecret))
	mac.Write([]byte(signatureOrigin))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Authorization token.
	authToken := fmt.Sprintf(`api_key="%s", algorithm="hmac-sha256", headers="host date request-line", signature="%s"`,
		t.cfg.APIKey, signature)
	authorization := base64.StdEncoding.EncodeToString([]byte(authToken))

	return fmt.Sprintf("https://%s%s?%s",
		host, path, url.Values{
			"authorization": {authorization},
			"date":          {date},
			"host":          {host},
		}.Encode())
}

// searchResult holds a single search result with title, snippet, and URL.
type searchResult struct {
	Title    string
	Snippet  string
	URL      string
	FullText string // original page content from search API
}

// extractResults parses the iFly search API response and returns structured results.
func extractResults(data []byte) ([]searchResult, error) {
	var resp struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("API error code %d: %s", resp.Code, resp.Msg)
	}

	// Try to find the results array from various response structures:
	// 1. {"data": {"results": [...]}}
	// 2. {"data": [...]}}
	// 3. {"data": {"items": [...]}}

	// Try object with known array keys.
	var dataObj map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &dataObj); err == nil {
		for _, key := range []string{"results", "items", "list", "records"} {
			if raw, ok := dataObj[key]; ok {
				if r := extractResultsFromArray(raw); len(r) > 0 {
					return r, nil
				}
			}
		}
		// Try all values.
		for _, raw := range dataObj {
			if r := extractResultsFromArray(raw); len(r) > 0 {
				return r, nil
			}
		}
	}

	// Try data as direct array.
	if r := extractResultsFromArray(resp.Data); len(r) > 0 {
		return r, nil
	}

	return nil, nil
}

// extractResultsFromArray extracts searchResult entries from a JSON array.
func extractResultsFromArray(data json.RawMessage) []searchResult {
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil
	}
	var results []searchResult
	for _, item := range items {
		u := getString(item, "url", "URL", "link", "href")
		if u == "" {
			continue
		}
		r := searchResult{
			URL:      u,
			Title:    getString(item, "title", "Title", "name", "Name"),
			Snippet:  getString(item, "snippet", "abstract", "description", "desc", "summary"),
			FullText: getString(item, "content", "text", "body", "full_text", "rawText", "raw_text", "pageContent"),
		}
		if r.Title == "" {
			r.Title = u
		}
		results = append(results, r)
	}
	return results
}

// getString returns the first non-empty string value found for any of the given keys.
func getString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// truncate returns the first n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

const webSearchDescription = `网页搜索：每个结果返回标题 + URL + 简短摘要。

这是两段式检索模式：
- 第 1 段（本工具）：拿到候选页面的标题 / URL / 简短摘要。
- 第 2 段（WebFetch）：摘要不够回答时，对挑中的 URL 调 WebFetch 拿全文。

使用规范：
- 传入一个搜索 query 找相关网页。
- 最多返回 N 条结果，每条含 Title / URL / 简短 Summary（通常 100~300 字）。
- 先快速扫摘要——对"是什么 / 谁 / 何时"这类事实型问题，摘要往往已经够答。
- 需要细节（正文全文、代码片段、原句）时，挑最相关的 URL 调 WebFetch。
- 不要对每个 URL 都 WebFetch——只在摘要确实不够时才取全文。

为什么先看摘要：
- 5 条结果的正文加起来 15KB 起步，会强制截断上下文。
- 摘要让视野更宽（看到全部结果），只对值得的 URL 付全文成本。

需要联网查信息时，先调本工具，再视情况 WebFetch。不要猜 URL——先搜索。`
