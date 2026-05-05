// Package webfetch implements the WebFetch tool for fetching and processing web content.
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

const (
	toolName     = "WebFetch"
	maxBodySize  = 1024 * 1024 // 1MB
	fetchTimeout = 30 * time.Second
)

// fetchInput is the JSON structure the LLM sends to invoke the tool.
type fetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

// fetchStats tracks error patterns for observability.
type fetchStats struct {
	mu          sync.Mutex
	total       int
	errors      int
	byStatus    map[int]int    // status_code → count
	byDomain    map[string]int // domain → error count
	networkErrs int
}

// WebFetchTool fetches content from URLs and returns it.
type WebFetchTool struct {
	tool.BaseTool
	cfg    config.ToolConfig
	client *http.Client
	logger *zap.Logger
	stats  fetchStats
}

// New creates a WebFetchTool with the given config.
func New(cfg config.ToolConfig, logger *zap.Logger) *WebFetchTool {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &WebFetchTool{
		cfg: cfg,
		client: &http.Client{
			Timeout: fetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		logger: logger.Named("webfetch"),
		stats: fetchStats{
			byStatus: make(map[int]int),
			byDomain: make(map[string]int),
		},
	}
}

func (t *WebFetchTool) Name() string            { return toolName }
func (t *WebFetchTool) Description() string     { return webFetchDescription }
func (t *WebFetchTool) IsReadOnly() bool                   { return true }
func (t *WebFetchTool) SafetyLevel() tool.SafetyLevel { return tool.SafetySafe }
func (t *WebFetchTool) IsConcurrencySafe() bool  { return true }
func (t *WebFetchTool) IsEnabled() bool          { return t.cfg.Enabled }

func (t *WebFetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "要抓取的 URL，必须公开可访问。",
				"format":      "uri",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "对抓取到的内容执行的 prompt（用于摘要/筛选）。",
			},
		},
		"required": []string{"url", "prompt"},
	}
}

func (t *WebFetchTool) ValidateInput(input json.RawMessage) error {
	var fi fetchInput
	if err := json.Unmarshal(input, &fi); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	if fi.URL == "" {
		return fmt.Errorf("url is required")
	}
	if fi.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	return nil
}

func (t *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var fi fetchInput
	if err := json.Unmarshal(input, &fi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Upgrade HTTP to HTTPS.
	rawURL := fi.URL
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + rawURL[7:]
	}

	// Extract domain for logging.
	domain := extractDomain(rawURL)

	t.stats.mu.Lock()
	t.stats.total++
	t.stats.mu.Unlock()

	t.logger.Info("webfetch attempt",
		zap.String("url", rawURL),
		zap.String("domain", domain),
	)

	// Fetch the URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.recordError(domain, 0)
		t.logger.Warn("webfetch request creation failed",
			zap.String("url", rawURL),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "error creating request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; HarnessClawEngine/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	start := time.Now()
	resp, err := t.client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		t.recordError(domain, 0)
		t.stats.mu.Lock()
		t.stats.networkErrs++
		t.stats.mu.Unlock()
		t.logger.Warn("webfetch network error",
			zap.String("url", rawURL),
			zap.String("domain", domain),
			zap.Duration("elapsed", elapsed),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "error fetching URL: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.recordError(domain, resp.StatusCode)
		t.logger.Warn("webfetch HTTP error",
			zap.String("url", rawURL),
			zap.String("domain", domain),
			zap.Int("status", resp.StatusCode),
			zap.Duration("elapsed", elapsed),
			zap.String("content_type", resp.Header.Get("Content-Type")),
		)
		return &types.ToolResult{
			Content: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status),
			IsError: true,
		}, nil
	}

	// Read body with size limit.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		t.logger.Warn("webfetch read error",
			zap.String("url", rawURL),
			zap.Error(err),
		)
		return &types.ToolResult{Content: "error reading response: " + err.Error(), IsError: true}, nil
	}

	content := string(body)

	// Basic HTML tag stripping for readability.
	content = stripBasicHTML(content)

	// Truncate if still too long.
	if len(content) > maxBodySize/2 {
		content = content[:maxBodySize/2] + "\n... (content truncated)"
	}

	t.logger.Info("webfetch success",
		zap.String("url", rawURL),
		zap.String("domain", domain),
		zap.Int("status", resp.StatusCode),
		zap.Int("body_bytes", len(body)),
		zap.Int("stripped_len", len(content)),
		zap.Duration("elapsed", elapsed),
	)

	return &types.ToolResult{
		Content: fmt.Sprintf("Fetched content from %s:\n\n%s\n\nPrompt: %s", rawURL, content, fi.Prompt),
		Metadata: map[string]any{
			"render_hint":    "markdown",
			"url":            rawURL,
			"status_code":    resp.StatusCode,
			"content_type":   resp.Header.Get("Content-Type"),
			"content_length": len(body),
		},
	}, nil
}

// recordError tracks error stats for observability.
func (t *WebFetchTool) recordError(domain string, statusCode int) {
	t.stats.mu.Lock()
	defer t.stats.mu.Unlock()
	t.stats.errors++
	if statusCode > 0 {
		t.stats.byStatus[statusCode]++
	}
	t.stats.byDomain[domain]++

	// Log cumulative stats periodically (every 10 errors).
	if t.stats.errors%10 == 0 {
		t.logger.Info("webfetch error stats",
			zap.Int("total_requests", t.stats.total),
			zap.Int("total_errors", t.stats.errors),
			zap.Int("network_errors", t.stats.networkErrs),
			zap.Any("by_status", t.stats.byStatus),
			zap.Any("by_domain_errors", t.stats.byDomain),
		)
	}
}

// extractDomain returns the hostname from a URL, or the raw URL on parse failure.
func extractDomain(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	return strings.ToLower(parsed.Hostname())
}

// stripBasicHTML removes common HTML tags for basic readability.
func stripBasicHTML(s string) string {
	// Remove script and style blocks.
	for _, tag := range []string{"script", "style"} {
		closeTag := "</" + tag + ">"
		for {
			lower := strings.ToLower(s)
			start := strings.Index(lower, "<"+tag)
			if start < 0 {
				break
			}
			end := strings.Index(lower[start:], closeTag)
			if end < 0 {
				break
			}
			cutEnd := start + end + len(closeTag)
			if cutEnd > len(s) {
				// Malformed HTML; remove from start to end of string.
				s = s[:start]
				break
			}
			s = s[:start] + s[cutEnd:]
		}
	}

	// Strip remaining tags.
	var result strings.Builder
	inTag := false
	for _, ch := range s {
		if ch == '<' {
			inTag = true
			continue
		}
		if ch == '>' {
			inTag = false
			result.WriteRune(' ')
			continue
		}
		if !inTag {
			result.WriteRune(ch)
		}
	}

	// Collapse whitespace.
	output := result.String()
	for strings.Contains(output, "  ") {
		output = strings.ReplaceAll(output, "  ", " ")
	}
	for strings.Contains(output, "\n\n\n") {
		output = strings.ReplaceAll(output, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(output)
}

const webFetchDescription = `抓取指定 URL 的内容并处理。

重要：本工具对登录后/私有 URL 会失败。调用前先确认 URL 是否需要登录或鉴权（如 Google Docs / Confluence / Jira / Slack / Notion / Figma）。需要鉴权的请勿使用本工具。

使用规范：
- 只用于你有把握能公开访问的 URL。
- URL 必须是完整有效的（含协议 + 域名 + 路径）。
- HTTP 会自动升级到 HTTPS。
- 不要猜或者编 URL——只用用户消息中给出的、或文档里提到的 URL。`
