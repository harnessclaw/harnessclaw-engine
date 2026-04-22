// Package tavilysearch provides a Tavily Search API tool.
package tavilysearch

import (
	"bytes"
	"context"
	"crypto/tls"
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
	toolName    = "TavilySearch"
	apiEndpoint = "https://api.tavily.com/search"
	maxBodySize = 2 << 20 // 2 MB — raw_content responses can be large

	// Truncation limits to keep LLM context reasonable.
	maxRawContentLen = 3000 // per result, ~750 tokens
	maxSnippetLen    = 1000 // per result snippet
)

// TavilySearchTool performs web searches via the Tavily API.
type TavilySearchTool struct {
	tool.BaseTool
	cfg    config.TavilySearchConfig
	client *http.Client
	logger *zap.Logger
}

// New creates a TavilySearchTool.
func New(cfg config.TavilySearchConfig, logger *zap.Logger) *TavilySearchTool {
	return &TavilySearchTool{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		},
		logger: logger.Named("tavily_search"),
	}
}

func (t *TavilySearchTool) Name() string           { return toolName }
func (t *TavilySearchTool) IsReadOnly() bool        { return true }
func (t *TavilySearchTool) IsConcurrencySafe() bool { return true }

func (t *TavilySearchTool) IsEnabled() bool {
	return t.cfg.Enabled && t.cfg.APIKey != ""
}

func (t *TavilySearchTool) Description() string {
	return `Search the web using the Tavily API. Returns relevant results with snippets.

When to use this tool:
- Questions about events, facts, or data after your knowledge cutoff
- User asks about "latest", "recent", "this year", or any time-sensitive topic
- You need to verify a fact you are not confident about
- User explicitly asks to search

When NOT to use:
- Programming syntax, math, logic — you already know these
- User says "don't search" or the answer is clearly within your training data

Two-stage pattern for deep research:
1. Call TavilySearch with include_raw_content=false (default) to get snippets + URLs
2. If a specific result needs deeper reading, call WebFetch on that URL
This avoids flooding context with full-text from all results.

Set include_raw_content=true only when you need full article text from ALL results
(e.g., "summarize these 3 articles"). This costs significantly more tokens.`
}

func (t *TavilySearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query. For best results, rewrite the user's question into an effective search query in the language most likely to yield results.",
				"minLength":   2,
			},
			"search_depth": map[string]any{
				"type":        "string",
				"description": "basic (default, 1 credit): single summary per URL. advanced (2 credits): multiple relevant chunks per URL, better for research.",
				"enum":        []string{"basic", "advanced", "fast"},
			},
			"topic": map[string]any{
				"type":        "string",
				"description": "general (default), news (real-time media), or finance",
				"enum":        []string{"general", "news", "finance"},
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Number of results (1-20, default 5)",
				"minimum":     1,
				"maximum":     20,
			},
			"time_range": map[string]any{
				"type":        "string",
				"description": "Filter by recency: day, week, month, or year",
				"enum":        []string{"day", "week", "month", "year"},
			},
			"include_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Only include results from these domains",
			},
			"exclude_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Exclude results from these domains",
			},
			"include_answer": map[string]any{
				"type":        "boolean",
				"description": "Include a short AI-generated answer (default false)",
			},
			"include_raw_content": map[string]any{
				"type":        "boolean",
				"description": "Include full cleaned page content per result in markdown. Significantly increases token usage. Use only when you need full article text from all results. Default false — prefer the two-stage pattern (search snippets, then WebFetch specific URLs).",
			},
		},
		"required": []string{"query"},
	}
}

func (t *TavilySearchTool) ValidateInput(input json.RawMessage) error {
	var p searchInput
	if err := json.Unmarshal(input, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Query) == "" {
		return fmt.Errorf("query is required")
	}
	return nil
}

// CheckPermission implements tool.PermissionPreChecker.
func (t *TavilySearchTool) CheckPermission(_ context.Context, _ json.RawMessage) tool.PermissionPreResult {
	return tool.PermissionPreResult{Behavior: "allow"}
}

func (t *TavilySearchTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var p searchInput
	if err := json.Unmarshal(input, &p); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	reqBody := apiRequest{
		Query:      p.Query,
		MaxResults: t.cfg.MaxResults,
	}
	if p.SearchDepth != "" {
		reqBody.SearchDepth = p.SearchDepth
	}
	if p.Topic != "" {
		reqBody.Topic = p.Topic
	}
	if p.MaxResults > 0 {
		reqBody.MaxResults = p.MaxResults
	}
	if reqBody.MaxResults == 0 {
		reqBody.MaxResults = 5
	}
	if p.TimeRange != "" {
		reqBody.TimeRange = p.TimeRange
	}
	if len(p.IncludeDomains) > 0 {
		reqBody.IncludeDomains = p.IncludeDomains
	}
	if len(p.ExcludeDomains) > 0 {
		reqBody.ExcludeDomains = p.ExcludeDomains
	}
	if p.IncludeAnswer {
		reqBody.IncludeAnswer = true
	}
	if p.IncludeRawContent {
		reqBody.IncludeRawContent = "markdown"
	}

	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiEndpoint, bytes.NewReader(body))
	if err != nil {
		return &types.ToolResult{Content: "failed to create request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.APIKey)

	t.logger.Debug("tavily search",
		zap.String("query", p.Query),
		zap.Bool("raw_content", p.IncludeRawContent),
	)

	resp, err := t.client.Do(req)
	if err != nil {
		return &types.ToolResult{Content: "request failed: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return &types.ToolResult{Content: "failed to read response: " + err.Error(), IsError: true}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return &types.ToolResult{
			Content: fmt.Sprintf("Tavily API error (HTTP %d): %s", resp.StatusCode, string(respBody)),
			IsError: true,
		}, nil
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return &types.ToolResult{Content: "failed to parse response: " + err.Error(), IsError: true}, nil
	}

	// Build metadata for WebSocket client (URLs for display).
	type urlEntry struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	urls := make([]urlEntry, len(apiResp.Results))
	for i, r := range apiResp.Results {
		urls[i] = urlEntry{URL: r.URL, Title: r.Title}
	}

	content := formatResults(&apiResp, p.IncludeRawContent)
	return &types.ToolResult{
		Content: content,
		Metadata: map[string]any{
			"render_hint":  "search",
			"query":        p.Query,
			"result_count": len(apiResp.Results),
			"urls":         urls,
			"has_raw":      p.IncludeRawContent,
		},
	}, nil
}

// ---------- input / API types ----------

type searchInput struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	TimeRange         string   `json:"time_range,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
	IncludeAnswer     bool     `json:"include_answer,omitempty"`
	IncludeRawContent bool     `json:"include_raw_content,omitempty"`
}

type apiRequest struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	MaxResults        int      `json:"max_results,omitempty"`
	TimeRange         string   `json:"time_range,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
	IncludeAnswer     bool     `json:"include_answer,omitempty"`
	IncludeRawContent string   `json:"include_raw_content,omitempty"` // "markdown" or "text"
}

type apiResponse struct {
	Query        string      `json:"query"`
	Answer       string      `json:"answer,omitempty"`
	Results      []apiResult `json:"results"`
	ResponseTime float64     `json:"response_time"`
}

type apiResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Content    string  `json:"content"`
	RawContent string  `json:"raw_content,omitempty"`
	Score      float64 `json:"score"`
}

// ---------- formatting ----------

func formatResults(resp *apiResponse, includeRaw bool) string {
	var buf strings.Builder

	if resp.Answer != "" {
		buf.WriteString("**Answer:** ")
		buf.WriteString(resp.Answer)
		buf.WriteString("\n\n")
	}

	if len(resp.Results) == 0 {
		buf.WriteString("No results found.")
		return buf.String()
	}

	for i, r := range resp.Results {
		fmt.Fprintf(&buf, "--- Result %d ---\n", i+1)
		fmt.Fprintf(&buf, "Title: %s\nURL: %s\n", r.Title, r.URL)

		if includeRaw && r.RawContent != "" {
			// Full content mode: truncate to keep context manageable.
			text := r.RawContent
			if len(text) > maxRawContentLen {
				text = text[:maxRawContentLen] + "\n... [truncated]"
			}
			buf.WriteString("Content:\n")
			buf.WriteString(text)
			buf.WriteString("\n")
		} else if r.Content != "" {
			// Snippet mode.
			text := r.Content
			if len(text) > maxSnippetLen {
				text = text[:maxSnippetLen] + "..."
			}
			buf.WriteString(text)
			buf.WriteString("\n")
		}
		buf.WriteString("\n")
	}

	fmt.Fprintf(&buf, "(%d results, %.2fs)", len(resp.Results), resp.ResponseTime)
	return buf.String()
}

