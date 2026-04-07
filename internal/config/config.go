// Package config loads and provides application configuration via Viper.
package config

import (
	"fmt"
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
	if c.Session.Storage != "memory" && c.Session.Storage != "sqlite" {
		return fmt.Errorf("session.storage must be 'memory' or 'sqlite', got %q", c.Session.Storage)
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
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level    string `mapstructure:"level"`    // debug, info, warn, error
	Format   string `mapstructure:"format"`   // json, console
	Output   string `mapstructure:"output"`   // stdout, file
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
type BifrostConfig struct {
	Enabled        bool   `mapstructure:"enabled"`
	Provider       string `mapstructure:"provider"`        // "anthropic", "openai", etc. Defaults to LLM.DefaultProvider.
	Model          string `mapstructure:"model"`            // Override model (defaults to provider's model).
	APIKey         string `mapstructure:"api_key"`          // Override API key (defaults to provider's key).
	BaseURL        string `mapstructure:"base_url"`         // Override base URL (defaults to provider's base_url).
	FallbackModel  string `mapstructure:"fallback_model"`   // Fallback model on primary failure.
	MaxConcurrency int    `mapstructure:"max_concurrency"`  // 0 = Bifrost default (1000).
	BufferSize     int    `mapstructure:"buffer_size"`       // 0 = Bifrost default (5000).
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
	Storage     string        `mapstructure:"storage"` // "memory" or "sqlite"
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
	WriteBuffer    int           `mapstructure:"write_buffer"`      // per-connection write buffer size (default 256)
	PingInterval   time.Duration `mapstructure:"ping_interval"`     // keep-alive ping interval (default 30s)
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`     // single write deadline (default 10s)
	MaxMessageSize int64         `mapstructure:"max_message_size"`  // max inbound frame size (default 512KB)
	ClientTools    bool          `mapstructure:"client_tools"`      // true = client executes tools; false = server executes tools
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
	Bash      ToolConfig `mapstructure:"bash"`
	FileRead  ToolConfig `mapstructure:"file_read"`
	FileEdit  ToolConfig `mapstructure:"file_edit"`
	FileWrite ToolConfig `mapstructure:"file_write"`
	Grep      ToolConfig `mapstructure:"grep"`
	Glob      ToolConfig `mapstructure:"glob"`
	WebFetch  ToolConfig `mapstructure:"web_fetch"`
}

// ToolConfig holds individual tool settings.
type ToolConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	Timeout     time.Duration `mapstructure:"timeout"`
	MaxFileSize string        `mapstructure:"max_file_size"`
	Sandbox     bool          `mapstructure:"sandbox"`
}

// PermissionConfig holds tool permission control settings.
type PermissionConfig struct {
	Mode         string   `mapstructure:"mode"`          // default, plan, bypass, acceptEdits, dontAsk
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
	v.SetDefault("session.storage", "sqlite")
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
	v.SetDefault("permission.mode", "default")

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

	return &cfg, nil
}
