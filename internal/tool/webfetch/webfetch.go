// Package webfetch implements the WebFetch tool for fetching and processing web content.
package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

// WebFetchTool fetches content from URLs and returns it.
type WebFetchTool struct {
	tool.BaseTool
	cfg    config.ToolConfig
	client *http.Client
}

// New creates a WebFetchTool with the given config.
func New(cfg config.ToolConfig) *WebFetchTool {
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
	}
}

func (t *WebFetchTool) Name() string                   { return toolName }
func (t *WebFetchTool) Description() string            { return webFetchDescription }
func (t *WebFetchTool) IsReadOnly() bool               { return true }
func (t *WebFetchTool) IsConcurrencySafe() bool        { return true }
func (t *WebFetchTool) IsEnabled() bool                { return t.cfg.Enabled }

func (t *WebFetchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch content from",
				"format":      "uri",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The prompt to run on the fetched content",
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
	// Upgrade HTTP to HTTPS.
	if strings.HasPrefix(fi.URL, "http://") {
		// Just validate, upgrade happens in Execute.
	}
	return nil
}

func (t *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (*types.ToolResult, error) {
	var fi fetchInput
	if err := json.Unmarshal(input, &fi); err != nil {
		return &types.ToolResult{Content: "invalid input: " + err.Error(), IsError: true}, nil
	}

	// Upgrade HTTP to HTTPS.
	url := fi.URL
	if strings.HasPrefix(url, "http://") {
		url = "https://" + url[7:]
	}

	// Fetch the URL.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &types.ToolResult{Content: "error creating request: " + err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ClaudeCode/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	resp, err := t.client.Do(req)
	if err != nil {
		return &types.ToolResult{Content: "error fetching URL: " + err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &types.ToolResult{
			Content: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status),
			IsError: true,
		}, nil
	}

	// Read body with size limit.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return &types.ToolResult{Content: "error reading response: " + err.Error(), IsError: true}, nil
	}

	content := string(body)

	// Basic HTML tag stripping for readability.
	// A proper implementation would use a HTML-to-markdown converter.
	content = stripBasicHTML(content)

	// Truncate if still too long.
	if len(content) > maxBodySize/2 {
		content = content[:maxBodySize/2] + "\n... (content truncated)"
	}

	return &types.ToolResult{
		Content: fmt.Sprintf("Fetched content from %s:\n\n%s\n\nPrompt: %s", url, content, fi.Prompt),
		Metadata: map[string]any{
			"url":            url,
			"status_code":    resp.StatusCode,
			"content_type":   resp.Header.Get("Content-Type"),
			"content_length": len(body),
		},
	}, nil
}

// stripBasicHTML removes common HTML tags for basic readability.
func stripBasicHTML(s string) string {
	// Remove script and style blocks.
	for _, tag := range []string{"script", "style"} {
		for {
			start := strings.Index(strings.ToLower(s), "<"+tag)
			if start < 0 {
				break
			}
			end := strings.Index(strings.ToLower(s[start:]), "</"+tag+">")
			if end < 0 {
				break
			}
			s = s[:start] + s[start+end+len("</"+tag+">"):]
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

const webFetchDescription = `Fetches content from a specified URL and processes it.

Usage:
- Takes a URL and a prompt as input
- Fetches the URL content and converts HTML to text
- The URL must be a fully-formed valid URL
- HTTP URLs will be automatically upgraded to HTTPS`
