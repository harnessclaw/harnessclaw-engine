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
func (t *TavilySearchTool) IsReadOnly() bool                   { return true }
func (t *TavilySearchTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *TavilySearchTool) IsConcurrencySafe() bool { return true }

func (t *TavilySearchTool) IsEnabled() bool {
	return t.cfg.Enabled && t.cfg.APIKey != ""
}

func (t *TavilySearchTool) Description() string {
	return `通过 Tavily API 搜索网页，返回带摘要的相关结果。

何时使用：
- 问题涉及训练截止后的事件 / 事实 / 数据。
- 用户提到"最新"、"近期"、"今年"或任何时效敏感话题。
- 你需要核实一个自己不太有把握的事实。
- 用户明确要求搜索。

不要使用：
- 编程语法、数学、逻辑——这些你已经会。
- 用户说"不要搜"或答案明显在你的训练数据里。

深度调研的两段式：
1. 先 TavilySearch include_raw_content=false（默认）拿摘要 + URL。
2. 若某条结果需要深读，对该 URL 调 WebFetch。
这样不会把全部结果的正文一次性灌进上下文。

include_raw_content=true 只在确实需要每条都拿全文时设（比如"汇总这 3 篇文章"），会显著消耗 token。`
}

func (t *TavilySearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "搜索 query。最佳实践：把用户问题改写成最易出结果的 query，并用最可能命中目标语料的语言。",
				"minLength":   2,
			},
			"search_depth": map[string]any{
				"type":        "string",
				"description": "basic（默认，1 credit）：每个 URL 一个摘要。advanced（2 credits）：每 URL 多段相关片段，更适合研究。",
				"enum":        []string{"basic", "advanced", "fast"},
			},
			"topic": map[string]any{
				"type":        "string",
				"description": "general（默认）/ news（实时媒体）/ finance（财经）。",
				"enum":        []string{"general", "news", "finance"},
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "结果数量（1~20，默认 5）。",
				"minimum":     1,
				"maximum":     20,
			},
			"time_range": map[string]any{
				"type":        "string",
				"description": "按时效过滤：day / week / month / year。",
				"enum":        []string{"day", "week", "month", "year"},
			},
			"include_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "只在这些域名内搜索。",
			},
			"exclude_domains": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "排除这些域名。",
			},
			"include_answer": map[string]any{
				"type":        "boolean",
				"description": "是否包含一段 AI 生成的简短答案（默认 false）。",
			},
			"include_raw_content": map[string]any{
				"type":        "boolean",
				"description": "是否在每条结果里返回 markdown 化的清洗后正文。会显著增加 token 用量。仅在确实需要每条都拿全文时启用。默认 false——更推荐两段式：先搜摘要，再对挑中的 URL 调 WebFetch。",
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

