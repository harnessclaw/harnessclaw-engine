// Package websearch implements the WebSearch tool using the iFlytek
// Spark Search v2 API (https://www.xfyun.cn/doc/spark/Search_API/search_API.html).
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// v2/search is a single fixed endpoint — no per-deployment routing,
	// so it's hardcoded rather than configurable. If iFly ever publishes
	// a regional alternate we can lift this back into config.
	searchEndpoint = "https://search-api-open.cn-huabei-1.xf-yun.com/v2/search"

	// queryMaxLen mirrors the documented upper bound on search_params.query.
	queryMaxLen = 512
)

// searchInput is the JSON structure the LLM sends to invoke the tool.
type searchInput struct {
	Query string `json:"query"`
}

// WebSearchTool performs web searches via the iFly v2/search API.
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
		},
		logger: logger.Named("websearch"),
	}
}

func (t *WebSearchTool) Name() string                  { return toolName }
func (t *WebSearchTool) Description() string           { return webSearchDescription }
func (t *WebSearchTool) IsReadOnly() bool              { return true }
func (t *WebSearchTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *WebSearchTool) IsConcurrencySafe() bool       { return true }

func (t *WebSearchTool) IsEnabled() bool {
	return t.cfg.Enabled && t.cfg.APIKey != ""
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
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInvalidInput}, nil
	}

	query := strings.TrimSpace(si.Query)
	if n := len([]rune(query)); n > queryMaxLen {
		query = string([]rune(query)[:queryMaxLen])
	}
	t.logger.Info("web search", zap.String("query", query))

	start := time.Now()

	limit := t.cfg.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	// v2/search body shape (search_params is required, enhance toggles
	// optional features). open_full_text=false keeps responses small —
	// the LLM only needs title/url/summary; if it wants full text it
	// follows up with WebFetch (see two-stage retrieval design below).
	body := map[string]any{
		"search_params": map[string]any{
			"query": query,
			"limit": limit,
			"enhance": map[string]any{
				"open_rerank":    true,
				"open_full_text": false,
			},
		},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return &types.ToolResult{Content: "error encoding request: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchEndpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return &types.ToolResult{Content: "error creating request: " + err.Error(), IsError: true, ErrorType: types.ToolErrorInternal}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.APIKey)

	resp, err := t.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.logger.Warn("web search request failed",
			zap.String("query", query),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		// Upstream HTTP transport failed (DNS / connection / TLS / read).
		// Classify as model_error — same bucket as upstream LLM hiccups,
		// retryable on the next dispatch.
		return &types.ToolResult{Content: "search request failed: " + err.Error(), IsError: true, ErrorType: types.ToolErrorModelError}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &types.ToolResult{Content: "error reading response: " + err.Error(), IsError: true, ErrorType: types.ToolErrorModelError}, nil
	}

	if resp.StatusCode != http.StatusOK {
		t.logger.Warn("web search HTTP error",
			zap.String("query", query),
			zap.Int("status", resp.StatusCode),
			zap.String("response", string(respBody)),
			zap.Duration("elapsed", elapsed),
		)
		errType := types.ToolErrorModelError
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			errType = types.ToolErrorRateLimit
		case http.StatusServiceUnavailable, 529:
			errType = types.ToolErrorOverloaded
		case http.StatusUnauthorized, http.StatusForbidden:
			errType = types.ToolErrorPermissionDenied
		}
		return &types.ToolResult{
			Content:   fmt.Sprintf("search API returned HTTP %d", resp.StatusCode),
			IsError:   true,
			ErrorType: errType,
		}, nil
	}

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
		errType := types.ToolErrorModelError
		// 11200 / 21009 / similar codes map to a credential/permission
		// problem — surface as permission_denied so the UI's "待配置"
		// path lights up instead of a generic retry-me message.
		if e, ok := err.(*apiError); ok {
			if e.code == "11200" || e.code == "21009" {
				errType = types.ToolErrorPermissionDenied
			} else if e.code == "11201" || e.code == "11202" || e.code == "11203" {
				errType = types.ToolErrorRateLimit
			}
		}
		return &types.ToolResult{Content: "error parsing search results: " + err.Error(), IsError: true, ErrorType: errType}, nil
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
		//   1. Snippet (v2 documents[].summary — already curated, ≤ ~300 chars)
		//   2. First MaxSummaryChars of FullText (defensive fallback for
		//      the rare case where open_full_text was on and summary empty)
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

// MaxSummaryChars caps the per-result summary length. v2 documents[].summary
// is usually 100-300 chars and won't be touched; the cap only fires on the
// defensive FullText fallback when open_full_text was on and summary empty.
const MaxSummaryChars = 500

// searchResult holds a single search result with title, snippet, and URL.
type searchResult struct {
	Title    string
	Snippet  string
	URL      string
	FullText string
}

// apiError carries the v2 err_code so callers can distinguish auth vs.
// quota vs. generic failure. err_code is a string in the documented
// response (e.g. "11200" = quota / auth).
type apiError struct {
	code string
	msg  string
}

func (e *apiError) Error() string {
	if e.msg == "" {
		return fmt.Sprintf("API error %s", e.code)
	}
	return fmt.Sprintf("API error %s: %s", e.code, e.msg)
}

// v2Response mirrors the documented v2/search response. Only the fields
// we actually read are typed; everything else is ignored.
type v2Response struct {
	Success bool   `json:"success"`
	ErrCode string `json:"err_code"`
	Message string `json:"message"`
	SID     string `json:"sid"`
	Data    struct {
		SearchResults struct {
			Documents []v2Document `json:"documents"`
		} `json:"search_results"`
	} `json:"data"`
}

type v2Document struct {
	Name          string `json:"name"`
	Summary       string `json:"summary"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	PublishedDate string `json:"published_date"`
}

// extractResults parses a v2/search response and returns structured results.
func extractResults(data []byte) ([]searchResult, error) {
	var resp v2Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	// v2 success criterion: err_code == "0". `success` is also returned
	// but treating err_code as authoritative matches the doc.
	if resp.ErrCode != "" && resp.ErrCode != "0" {
		return nil, &apiError{code: resp.ErrCode, msg: resp.Message}
	}

	docs := resp.Data.SearchResults.Documents
	out := make([]searchResult, 0, len(docs))
	for _, d := range docs {
		if d.URL == "" {
			continue
		}
		title := d.Name
		if title == "" {
			title = d.URL
		}
		out = append(out, searchResult{
			Title:    title,
			URL:      d.URL,
			Snippet:  d.Summary,
			FullText: d.Content,
		})
	}
	return out, nil
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
