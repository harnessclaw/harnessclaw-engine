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
func (t *WebSearchTool) IsReadOnly() bool         { return true }
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
				"description": "The search query",
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

	// Build Content for LLM: full text of each result.
	// Build Metadata for WebSocket client: URLs for display.
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

		// Prefer full text; fall back to snippet.
		text := r.FullText
		if text == "" {
			text = r.Snippet
		}
		if text != "" {
			sb.WriteString(fmt.Sprintf("Content:\n%s\n", text))
		}
		sb.WriteString("\n")

		urlEntries = append(urlEntries, urlEntry{URL: r.URL, Title: r.Title})
	}

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

const webSearchDescription = `Searches the web and returns relevant results with full content.

Usage:
- Provide a search query to find relevant web pages
- Returns search results with title, URL, and full page content
- You can directly use the returned content to answer questions without calling WebFetch
- Only use WebFetch if the search result content is insufficient and you need more detail from a specific URL

Use this tool BEFORE WebFetch when you need to find information online. Do not guess URLs — search first.`
