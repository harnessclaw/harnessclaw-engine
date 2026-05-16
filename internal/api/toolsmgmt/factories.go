package toolsmgmt

import (
	"fmt"

	"go.uber.org/zap"

	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/tavilysearch"
	"harnessclaw-go/internal/tool/websearch"
)

func init() {
	factories["web_search"] = &factory{
		registeredName:   "WebSearch",
		credentialFields: []string{"api_key", "api_secret", "app_id"},
		snapshot: func(c *config.Config) map[string]any {
			ws := c.Tools.WebSearch
			return map[string]any{
				"enabled":    ws.Enabled,
				"api_key":    ws.APIKey,
				"api_secret": ws.APISecret,
				"app_id":     ws.AppID,
				"host":       ws.Host,
				"path":       ws.Path,
				"limit":      ws.Limit,
			}
		},
		apply: func(raw map[string]any, logger *zap.Logger) (tool.Tool, config.ToolsConfig, error) {
			cfg := config.WebSearchConfig{
				Enabled:   asBool(raw["enabled"]),
				APIKey:    asString(raw["api_key"]),
				APISecret: asString(raw["api_secret"]),
				AppID:     asString(raw["app_id"]),
				Host:      asString(raw["host"]),
				Path:      asString(raw["path"]),
				Limit:     asInt(raw["limit"]),
			}
			if cfg.Enabled {
				missing := []string{}
				if cfg.APIKey == "" {
					missing = append(missing, "api_key")
				}
				if cfg.APISecret == "" {
					missing = append(missing, "api_secret")
				}
				if cfg.AppID == "" {
					missing = append(missing, "app_id")
				}
				if len(missing) > 0 {
					return nil, config.ToolsConfig{}, fmt.Errorf("web_search enabled but missing credentials: %v", missing)
				}
			}
			// Return a fresh config.ToolsConfig the handler will assign back to cfg.Tools.
			return websearch.New(cfg, logger), config.ToolsConfig{WebSearch: cfg}, nil
		},
	}

	factories["tavily_search"] = &factory{
		registeredName:   "TavilySearch",
		credentialFields: []string{"api_key"},
		snapshot: func(c *config.Config) map[string]any {
			ts := c.Tools.TavilySearch
			return map[string]any{
				"enabled":     ts.Enabled,
				"api_key":     ts.APIKey,
				"max_results": ts.MaxResults,
			}
		},
		apply: func(raw map[string]any, logger *zap.Logger) (tool.Tool, config.ToolsConfig, error) {
			cfg := config.TavilySearchConfig{
				Enabled:    asBool(raw["enabled"]),
				APIKey:     asString(raw["api_key"]),
				MaxResults: asInt(raw["max_results"]),
			}
			if cfg.Enabled && cfg.APIKey == "" {
				return nil, config.ToolsConfig{}, fmt.Errorf("tavily_search enabled but missing credentials: [api_key]")
			}
			return tavilysearch.New(cfg, logger), config.ToolsConfig{TavilySearch: cfg}, nil
		},
	}
}

// asString coerces a JSON-decoded value to string. Missing / wrong type → "".
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// asBool coerces a JSON-decoded value to bool. Missing / wrong type → false.
func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// asInt coerces a JSON-decoded numeric (float64) to int. Missing / wrong type → 0.
func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	}
	return 0
}
