// Package config loads and provides application configuration via Viper.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Validate checks that the loaded configuration contains valid values.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535, got %d", c.Server.Port)
	}
	if c.LLM.DefaultProvider == "" {
		return fmt.Errorf("llm.default_provider must not be empty")
	}
	if c.Engine.MaxTurns < 1 {
		return fmt.Errorf("engine.max_turns must be at least 1, got %d", c.Engine.MaxTurns)
	}
	if c.Engine.AutoCompactThreshold < 0 || c.Engine.AutoCompactThreshold > 1.0 {
		return fmt.Errorf("engine.auto_compact_threshold must be between 0 and 1, got %f", c.Engine.AutoCompactThreshold)
	}
	validModes := map[string]bool{
		"default": true, "plan": true, "bypass": true, "acceptEdits": true, "dontAsk": true,
	}
	if c.Permission.Mode != "" && !validModes[c.Permission.Mode] {
		return fmt.Errorf("permission.mode must be one of default/plan/bypass/acceptEdits/dontAsk, got %q", c.Permission.Mode)
	}
	if c.LLM.MaxRetries < 0 {
		return fmt.Errorf("llm.max_retries must be non-negative, got %d", c.LLM.MaxRetries)
	}
	return nil
}

// Config is the top-level application configuration.
type Config struct {
	Server     ServerConfig     `mapstructure:"server"`
	Log        LogConfig        `mapstructure:"log"`
	LLM        LLMConfig        `mapstructure:"llm"`
	Engine     EngineConfig     `mapstructure:"engine"`
	Session    SessionConfig    `mapstructure:"session"`
	Channel    ChannelConfig    `mapstructure:"channels"`
	Tools      ToolsConfig      `mapstructure:"tools"`
	Permission PermissionConfig `mapstructure:"permission"`
	Skills     SkillsConfig     `mapstructure:"skills"`
}

// SkillsConfig holds skill loading settings.
type SkillsConfig struct {
	// Dirs is the list of directories to load skills from.
	// Each directory is scanned for SKILL.md (directory format) or *.md (flat format).
	// Earlier entries have higher priority on name conflict.
	Dirs []string `mapstructure:"dirs"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level    string `mapstructure:"level"`  // debug, info, warn, error
	Format   string `mapstructure:"format"` // json, console
	Output   string `mapstructure:"output"` // stdout, file
	FilePath string `mapstructure:"file_path"`
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	DefaultProvider string                    `mapstructure:"default_provider"`
	Providers       map[string]ProviderConfig `mapstructure:"providers"`
	Bifrost         BifrostConfig             `mapstructure:"bifrost"`
	MaxRetries      int                       `mapstructure:"max_retries"`
	APITimeout      time.Duration             `mapstructure:"api_timeout"`
	ProxyURL        string                    `mapstructure:"proxy_url"`
	CustomHeaders   map[string]string         `mapstructure:"custom_headers"`
}

// ProviderConfig holds a single provider's settings.
type ProviderConfig struct {
	APIKey      string  `mapstructure:"api_key"`
	Model       string  `mapstructure:"model"`
	MaxTokens   int     `mapstructure:"max_tokens"`
	Temperature float64 `mapstructure:"temperature"`
	BaseURL     string  `mapstructure:"base_url"`
}

// BifrostConfig holds Bifrost unified SDK settings.
// Bifrost is always enabled as the sole provider backend.
type BifrostConfig struct {
	Provider       string `mapstructure:"provider"`        // "anthropic", "openai", etc. Defaults to LLM.DefaultProvider.
	Model          string `mapstructure:"model"`           // Override model (defaults to provider's model).
	APIKey         string `mapstructure:"api_key"`         // Override API key (defaults to provider's key).
	BaseURL        string `mapstructure:"base_url"`        // Override base URL (defaults to provider's base_url).
	FallbackModel  string `mapstructure:"fallback_model"`  // Fallback model on primary failure.
	MaxConcurrency int    `mapstructure:"max_concurrency"` // 0 = Bifrost default (1000).
	BufferSize     int    `mapstructure:"buffer_size"`     // 0 = Bifrost default (5000).
}

// EngineConfig holds query engine settings.
type EngineConfig struct {
	MaxTurns             int           `mapstructure:"max_turns"`
	AutoCompactThreshold float64       `mapstructure:"auto_compact_threshold"`
	ToolTimeout          time.Duration `mapstructure:"tool_timeout"`
}

// SessionConfig holds session management settings.
type SessionConfig struct {
	MaxMessages int           `mapstructure:"max_messages"`
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`
	DBPath      string        `mapstructure:"db_path"` // SQLite database file path
}

// ChannelConfig holds per-channel settings.
type ChannelConfig struct {
	Feishu    FeishuChannelConfig `mapstructure:"feishu"`
	WebSocket WSChannelConfig     `mapstructure:"websocket"`
	HTTP      HTTPChannelConfig   `mapstructure:"http"`
}

// FeishuChannelConfig holds Feishu bot settings.
type FeishuChannelConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Host      string `mapstructure:"host"`
	Port      int    `mapstructure:"port"`
	AppID     string `mapstructure:"app_id"`
	AppSecret string `mapstructure:"app_secret"`
}

// WSChannelConfig holds WebSocket settings.
type WSChannelConfig struct {
	Enabled        bool          `mapstructure:"enabled"`
	Host           string        `mapstructure:"host"`
	Port           int           `mapstructure:"port"`
	Path           string        `mapstructure:"path"`
	WriteBuffer    int           `mapstructure:"write_buffer"`     // per-connection write buffer size (default 256)
	PingInterval   time.Duration `mapstructure:"ping_interval"`    // keep-alive ping interval (default 30s)
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`    // single write deadline (default 10s)
	MaxMessageSize int64         `mapstructure:"max_message_size"` // max inbound frame size (default 512KB)
	ClientTools    bool          `mapstructure:"client_tools"`     // true = client executes tools; false = server executes tools
}

// HTTPChannelConfig holds HTTP API settings.
type HTTPChannelConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
	Path    string `mapstructure:"path"`
}

// ToolsConfig holds per-tool settings.
type ToolsConfig struct {
	Bash      ToolConfig      `mapstructure:"bash"`
	FileRead  ToolConfig      `mapstructure:"file_read"`
	FileEdit  ToolConfig      `mapstructure:"file_edit"`
	FileWrite ToolConfig      `mapstructure:"file_write"`
	Grep      ToolConfig      `mapstructure:"grep"`
	Glob      ToolConfig      `mapstructure:"glob"`
	WebFetch  ToolConfig      `mapstructure:"web_fetch"`
	WebSearch    WebSearchConfig    `mapstructure:"web_search"`
	TavilySearch TavilySearchConfig `mapstructure:"tavily_search"`
}

// ToolConfig holds individual tool settings.
type ToolConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Timeout     time.Duration `mapstructure:"timeout"`
	MaxFileSize string        `mapstructure:"max_file_size"`
	Sandbox     bool          `mapstructure:"sandbox"`
}

// WebSearchConfig holds settings for the iFly web search tool.
type WebSearchConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	APIKey    string `mapstructure:"api_key"`
	APISecret string `mapstructure:"api_secret"`
	AppID     string `mapstructure:"app_id"`
	Host      string `mapstructure:"host"`
	Path      string `mapstructure:"path"`
	Limit     int    `mapstructure:"limit"`
}

// TavilySearchConfig holds settings for the Tavily Search API tool.
type TavilySearchConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	APIKey     string `mapstructure:"api_key"`
	MaxResults int    `mapstructure:"max_results"`
}

// PermissionConfig holds tool permission control settings.
type PermissionConfig struct {
	Mode         string   `mapstructure:"mode"` // default, plan, bypass, acceptEdits, dontAsk
	AllowedTools []string `mapstructure:"allowed_tools"`
	DeniedTools  []string `mapstructure:"denied_tools"`
}

// Load reads configuration from file, environment variables, and defaults.
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.output", "stdout")
	v.SetDefault("llm.default_provider", "anthropic")
	v.SetDefault("llm.max_retries", 10)
	v.SetDefault("llm.api_timeout", "600s")
	v.SetDefault("engine.max_turns", 50)
	v.SetDefault("engine.auto_compact_threshold", 0.8)
	v.SetDefault("engine.tool_timeout", "120s")
	v.SetDefault("session.max_messages", 200)
	v.SetDefault("session.idle_timeout", "30m")
	v.SetDefault("session.db_path", "~/.harnessclaw/db/sessions.db")
	v.SetDefault("channels.websocket.enabled", true)
	v.SetDefault("channels.websocket.host", "0.0.0.0")
	v.SetDefault("channels.websocket.port", 8081)
	v.SetDefault("channels.websocket.path", "/ws")
	v.SetDefault("channels.websocket.write_buffer", 256)
	v.SetDefault("channels.websocket.ping_interval", "30s")
	v.SetDefault("channels.websocket.write_timeout", "10s")
	v.SetDefault("channels.websocket.max_message_size", 524288)
	v.SetDefault("channels.http.enabled", true)
	v.SetDefault("channels.http.host", "0.0.0.0")
	v.SetDefault("channels.http.port", 8080)
	v.SetDefault("channels.http.path", "/api/v1")
	v.SetDefault("channels.feishu.enabled", false)
	v.SetDefault("channels.feishu.host", "0.0.0.0")
	v.SetDefault("channels.feishu.port", 8082)
	v.SetDefault("tools.bash.enabled", true)
	v.SetDefault("tools.bash.timeout", "60s")
	v.SetDefault("tools.file_read.enabled", true)
	v.SetDefault("tools.file_edit.enabled", true)
	v.SetDefault("tools.file_write.enabled", true)
	v.SetDefault("tools.grep.enabled", true)
	v.SetDefault("tools.glob.enabled", true)
	v.SetDefault("tools.web_fetch.enabled", true)
	v.SetDefault("tools.web_search.enabled", false)
	v.SetDefault("tools.web_search.host", "cbm-search-api.cn-huabei-1.xf-yun.com")
	v.SetDefault("tools.web_search.path", "/biz/search")
	v.SetDefault("tools.web_search.limit", 5)
	v.SetDefault("tools.tavily_search.enabled", false)
	v.SetDefault("tools.tavily_search.max_results", 5)
	v.SetDefault("permission.mode", "default")
	v.SetDefault("skills.dirs", []string{"~/.harnessclaw/workspace/skills"})

	// Config file
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	// Environment variables: e.g. CLAUDE_SERVER_PORT -> server.port
	v.SetEnvPrefix("CLAUDE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// Config file not found is OK — use defaults + env vars
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Apply default skills directory when config has no dirs.
	// Viper's SetDefault won't help if the key exists but the value is null/empty.
	if len(cfg.Skills.Dirs) == 0 {
		home, _ := os.UserHomeDir()
		cfg.Skills.Dirs = []string{filepath.Join(home, ".harnessclaw", "workspace", "skills")}
	}

	// Expand ~ in skill dirs to the user's home directory.
	expandSkillDirs(&cfg)

	// Expand ~ in database paths to the user's home directory.
	expandHomePath(&cfg.Session.DBPath)

	return &cfg, nil
}

// expandHomePath replaces a ~ prefix with the user's home directory.
func expandHomePath(p *string) {
	if *p == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	if *p == "~" {
		*p = home
		return
	}
	if strings.HasPrefix(*p, "~/") || strings.HasPrefix(*p, "~\\") {
		*p = filepath.Join(home, filepath.FromSlash((*p)[2:]))
	}
}

// expandSkillDirs replaces ~ prefix with the user's home directory in skill paths,
// and normalizes path separators for the current platform.
func expandSkillDirs(cfg *Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	for i, dir := range cfg.Skills.Dirs {
		if dir == "~" {
			cfg.Skills.Dirs[i] = home
			continue
		}
		// Match both ~/path and ~\path (Windows)
		if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~\\") {
			// Split the relative part after ~/ or ~\, then rejoin platform-aware
			rel := filepath.FromSlash(dir[2:])
			cfg.Skills.Dirs[i] = filepath.Join(home, rel)
			continue
		}
		// Normalize any forward slashes in explicit paths (e.g. from yaml on Windows)
		cfg.Skills.Dirs[i] = filepath.FromSlash(dir)
	}
}
