// Package main is the entry point for the harnessclaw-engine service.
//
// Startup sequence:
//  1. Load configuration (Viper)
//  2. Initialize structured logger (Zap)
//  3. Initialize storage (SQLite or memory)
//  4. Create event bus
//  5. Register tools
//  6. Initialize LLM provider (Bifrost SDK)
//  7. Create session manager
//  8. Create query engine
//  9. Build router with middleware chain
//  10. Start channels (Feishu, WebSocket, HTTP) in parallel
//  11. Wait for shutdown signal, then gracefully stop
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"harnessclaw-go/internal/channel"
	wsch "harnessclaw-go/internal/channel/websocket"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/router"
	"harnessclaw-go/internal/router/middleware"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/storage"
	"harnessclaw-go/internal/storage/memory"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/bash"
	"harnessclaw-go/internal/tool/fileedit"
	"harnessclaw-go/internal/tool/fileread"
	"harnessclaw-go/internal/tool/filewrite"
	"harnessclaw-go/internal/tool/glob"
	"harnessclaw-go/internal/tool/grep"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/internal/tool/webfetch"
	"harnessclaw-go/pkg/types"
)

// gracefulShutdownTimeout is the maximum duration for the shutdown sequence.
const gracefulShutdownTimeout = 30 * time.Second

// idleCleanupInterval is how often the session manager checks for idle sessions.
const idleCleanupInterval = 5 * time.Minute

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// --- Step 1: Load configuration ---
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Validate configuration after loading.
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	// --- Step 2: Initialize logger ---
	logger, err := initLogger(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("starting harnessclaw-engine",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.String("storage", cfg.Session.Storage),
		zap.String("default_provider", cfg.LLM.DefaultProvider),
	)

	// --- Step 3: Initialize storage ---
	store, err := initStorage(cfg.Session, logger)
	if err != nil {
		logger.Fatal("failed to initialize storage", zap.Error(err))
	}
	defer store.Close()
	logger.Info("storage initialized", zap.String("backend", cfg.Session.Storage))

	// --- Step 4: Create event bus and subscribe key events for logging ---
	bus := event.NewBus()
	subscribeEventLogging(bus, logger)

	// --- Step 5: Register tools ---
	registry := tool.NewRegistry()

	// Register built-in tools based on config.
	builtInTools := []struct {
		enabled bool
		factory func() tool.Tool
	}{
		{cfg.Tools.Bash.Enabled, func() tool.Tool { return bash.New(cfg.Tools.Bash) }},
		{cfg.Tools.FileRead.Enabled, func() tool.Tool { return fileread.New(cfg.Tools.FileRead) }},
		{cfg.Tools.FileEdit.Enabled, func() tool.Tool { return fileedit.New(cfg.Tools.FileEdit) }},
		{cfg.Tools.FileWrite.Enabled, func() tool.Tool { return filewrite.New(cfg.Tools.FileWrite) }},
		{cfg.Tools.Grep.Enabled, func() tool.Tool { return grep.New(cfg.Tools.Grep) }},
		{cfg.Tools.Glob.Enabled, func() tool.Tool { return glob.New(cfg.Tools.Glob) }},
		{cfg.Tools.WebFetch.Enabled, func() tool.Tool { return webfetch.New(cfg.Tools.WebFetch) }},
	}
	for _, bt := range builtInTools {
		if bt.enabled {
			if err := registry.Register(bt.factory()); err != nil {
				logger.Fatal("failed to register tool", zap.Error(err))
			}
		}
	}

	// Load skills and register SkillTool.
	skillLoader := skill.NewLoader(cfg.Skills.Dirs, logger)
	skillCommands, err := skillLoader.LoadAll()
	if err != nil {
		logger.Warn("skill loading had issues", zap.Error(err))
	}
	for i, cmd := range skillCommands {
		base := cmd.GetBase()
		if base == nil {
			continue
		}
		logger.Info("skill command detail",
			zap.Int("index", i),
			zap.String("name", base.Name),
			zap.String("description", base.Description),
			zap.String("when_to_use", base.WhenToUse),
			zap.Strings("aliases", base.Aliases),
			zap.Int("source", int(base.Source)),
			zap.String("loaded_from", string(base.LoadedFrom)),
			zap.Bool("user_invocable", base.UserInvocable),
			zap.Bool("disable_model_invocation", base.DisableModelInvocation),
			zap.String("type", string(cmd.Type)),
		)
		if cmd.Prompt != nil {
			logger.Info("skill prompt detail",
				zap.Int("index", i),
				zap.String("name", base.Name),
				zap.String("model", cmd.Prompt.Model),
				zap.String("effort", cmd.Prompt.Effort),
				zap.String("context", cmd.Prompt.Context),
				zap.String("agent", cmd.Prompt.Agent),
				zap.Strings("allowed_tools", cmd.Prompt.AllowedTools),
				zap.Strings("arg_names", cmd.Prompt.ArgNames),
				zap.Strings("paths", cmd.Prompt.Paths),
				zap.String("skill_root", cmd.Prompt.SkillRoot),
			)
		}
	}
	cmdRegistry := command.NewRegistry()
	cmdRegistry.LoadAll(skillCommands)
	if err := registry.Register(skilltool.New(cmdRegistry)); err != nil {
		logger.Fatal("failed to register skill tool", zap.Error(err))
	}

	logger.Info("tool registry initialized",
		zap.Int("tool_count", len(registry.All())),
		zap.Int("skill_count", len(skillCommands)),
	)

	// --- Step 6: Initialize LLM provider ---
	llmProvider := initProvider(cfg.LLM, logger)
	logger.Info("LLM provider initialized", zap.String("provider", llmProvider.Name()))

	// --- Step 7: Create session manager ---
	sessionMgr := session.NewManager(store, logger, cfg.Session.IdleTimeout)
	logger.Info("session manager initialized")

	// Start periodic idle session cleanup.
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()
	go runIdleCleanup(cleanupCtx, sessionMgr, logger)

	// --- Step 8: Create query engine ---
	compactor := compact.NewLLMCompactor(llmProvider, logger)
	permChecker := initPermissionChecker(cfg.Permission)

	// Build system prompt — generic skill guidance only (Layer 1 of 3-layer skill injection).
	// The actual skill listing is injected per-turn as a <system-reminder> message (Layer 3).
	hasSkills := len(cmdRegistry.GetSkillToolCommands()) > 0
	systemPrompt := "You are a helpful assistant."

	if hasSkills {
		systemPrompt += "\n\n# Session-specific guidance\n" +
			" - /<skill-name> (e.g., /commit) is shorthand for users to invoke a user-invocable skill. " +
			"When executed, the skill gets expanded to a full prompt. Use the Skill tool to execute them. " +
			"IMPORTANT: Only use Skill for skills listed in its user-invocable skills section - do not guess or use built-in CLI commands.\n"
	}
	systemPrompt += " - Skills directories: " + strings.Join(cfg.Skills.Dirs, ", ")

	engCfg := engine.QueryEngineConfig{
		MaxTurns:             cfg.Engine.MaxTurns,
		AutoCompactThreshold: cfg.Engine.AutoCompactThreshold,
		ToolTimeout:          cfg.Engine.ToolTimeout,
		MaxTokens:            16384,
		SystemPrompt:         systemPrompt,
	}
	eng := engine.NewQueryEngine(llmProvider, registry, sessionMgr, compactor, permChecker, bus, logger, engCfg, cmdRegistry)
	logger.Info("engine initialized",
		zap.Int("max_turns", engCfg.MaxTurns),
		zap.Float64("compact_threshold", engCfg.AutoCompactThreshold),
	)

	// --- Step 9: Build router with middleware chain ---
	middlewares := buildMiddlewareChain(cfg, logger)
	channels := make(map[string]channel.Channel)

	// Register WebSocket channel if enabled.
	if cfg.Channel.WebSocket.Enabled {
		wsCh := wsch.New(cfg.Channel.WebSocket, nil, logger)
		channels["websocket"] = wsCh
		logger.Info("websocket channel registered",
			zap.String("addr", fmt.Sprintf("%s:%d", cfg.Channel.WebSocket.Host, cfg.Channel.WebSocket.Port)),
			zap.String("path", cfg.Channel.WebSocket.Path),
		)
	}

	rtr := router.New(eng, channels, middlewares, logger)
	logger.Info("router initialized",
		zap.Int("middleware_count", len(middlewares)),
		zap.Int("channel_count", len(channels)),
	)

	// --- Step 10: Start channels ---
	channelCtx, channelCancel := context.WithCancel(context.Background())
	defer channelCancel()
	for name, ch := range channels {
		go func(n string, c channel.Channel) {
			if err := c.Start(channelCtx, rtr.Handle); err != nil {
				logger.Error("channel exited with error", zap.String("channel", n), zap.Error(err))
			}
		}(name, ch)
	}
	// TODO: Start HTTP and Feishu channels when implementations are ready.
	logger.Info("all channels started, service is ready")

	// --- Step 11: Wait for shutdown signal (double-signal support) ---
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// First signal: begin graceful shutdown.
	<-sigCh
	logger.Info("shutdown signal received, stopping gracefully...")

	// Second signal: force exit.
	go func() {
		<-sigCh
		logger.Warn("forced shutdown on second signal")
		os.Exit(1)
	}()

	// --- Graceful shutdown sequence ---
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer shutdownCancel()

	// Stop accepting new messages and cancel idle cleanup.
	cleanupCancel()
	channelCancel()

	// Stop all channels in parallel with timeout.
	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(c channel.Channel) {
			defer wg.Done()
			if err := c.Stop(shutdownCtx); err != nil {
				logger.Error("channel stop error", zap.String("channel", c.Name()), zap.Error(err))
			}
		}(ch)
	}
	wg.Wait()
	logger.Info("channels stopped")

	// Wait for in-flight queries to complete (grace period).
	// The engine's placeholder does not track in-flight queries; real
	// implementation should use a WaitGroup or similar mechanism.
	logger.Info("waiting for in-flight queries to drain")
	select {
	case <-shutdownCtx.Done():
		logger.Warn("shutdown timeout reached, some queries may not have completed")
	case <-time.After(2 * time.Second):
		// Grace period for in-flight queries.
	}

	// Persist all active sessions.
	logger.Info("persisting active sessions")
	if err := sessionMgr.PersistAll(shutdownCtx); err != nil {
		logger.Error("failed to persist some sessions", zap.Error(err))
	}

	// Close storage.
	if err := store.Close(); err != nil {
		logger.Error("failed to close storage", zap.Error(err))
	}

	// Flush logger.
	logger.Info("shutdown complete")
	_ = logger.Sync()
}

// initLogger creates a Zap logger from config.
func initLogger(cfg config.LogConfig) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		// Warn via stderr since logger isn't ready yet.
		fmt.Fprintf(os.Stderr, "warning: invalid log level %q, defaulting to info\n", cfg.Level)
		level = zapcore.InfoLevel
	}

	var zapCfg zap.Config
	if cfg.Format == "console" {
		zapCfg = zap.NewDevelopmentConfig()
	} else {
		zapCfg = zap.NewProductionConfig()
	}
	zapCfg.Level = zap.NewAtomicLevelAt(level)

	if cfg.Output == "file" && cfg.FilePath != "" {
		zapCfg.OutputPaths = []string{cfg.FilePath}
	}

	return zapCfg.Build()
}

// initStorage creates the appropriate Storage implementation based on config.
func initStorage(cfg config.SessionConfig, logger *zap.Logger) (storage.Storage, error) {
	switch cfg.Storage {
	case "memory":
		logger.Info("using in-memory storage (data will be lost on restart)")
		return memory.New(), nil
	case "sqlite":
		// TODO: Replace with real SQLite storage once internal/storage/sqlite is implemented.
		// For now, fall back to memory storage with a warning.
		logger.Warn("sqlite storage not yet implemented, falling back to memory storage")
		return memory.New(), nil
	default:
		return nil, fmt.Errorf("unknown storage type: %s", cfg.Storage)
	}
}

// subscribeEventLogging registers event bus handlers that log key events.
func subscribeEventLogging(bus *event.Bus, logger *zap.Logger) {
	bus.Subscribe(event.TopicSessionCreated, func(evt event.Event) {
		logger.Info("event: session created", zap.Any("payload", evt.Payload))
	})

	bus.Subscribe(event.TopicSessionArchived, func(evt event.Event) {
		logger.Info("event: session archived", zap.Any("payload", evt.Payload))
	})

	bus.Subscribe(event.TopicToolExecuted, func(evt event.Event) {
		logger.Debug("event: tool executed", zap.Any("payload", evt.Payload))
	})

	bus.Subscribe(event.TopicQueryStarted, func(evt event.Event) {
		logger.Debug("event: query started", zap.Any("payload", evt.Payload))
	})

	bus.Subscribe(event.TopicQueryCompleted, func(evt event.Event) {
		logger.Info("event: query completed", zap.Any("payload", evt.Payload))
	})

	bus.Subscribe(event.TopicCompactTriggered, func(evt event.Event) {
		logger.Info("event: compact triggered", zap.Any("payload", evt.Payload))
	})
}

// buildMiddlewareChain assembles the middleware stack based on configuration.
func buildMiddlewareChain(cfg *config.Config, logger *zap.Logger) []middleware.Middleware {
	var chain []middleware.Middleware

	// 1. Logging middleware (outermost, captures timing for all requests).
	chain = append(chain, middleware.Logging(logger))

	// 2. Authentication middleware.
	// TODO: Replace with real JWT/API-key validator when auth module is implemented.
	authValidator := func(_ context.Context, msg *types.IncomingMessage) bool {
		// Placeholder: allow all requests in development.
		_ = msg
		return true
	}
	chain = append(chain, middleware.Auth(authValidator))

	// 3. Rate limiting middleware.
	chain = append(chain, middleware.RateLimit(60, 1*time.Minute))

	return chain
}

// initPermissionChecker creates a permission checker from config.
func initPermissionChecker(cfg config.PermissionConfig) permission.Checker {
	mode := permission.Mode(cfg.Mode)
	if mode == "" {
		mode = permission.ModeDefault
	}
	if mode == permission.ModeBypass {
		return permission.BypassChecker{}
	}

	var rules []permission.Rule
	for _, toolName := range cfg.AllowedTools {
		rules = append(rules, permission.Rule{
			Source:   permission.SourceSession,
			Behavior: permission.BehaviorAllow,
			ToolName: toolName,
		})
	}
	for _, toolName := range cfg.DeniedTools {
		rules = append(rules, permission.Rule{
			Source:   permission.SourceSession,
			Behavior: permission.BehaviorDeny,
			ToolName: toolName,
		})
	}

	return permission.NewOuterChecker(mode, rules)
}

// runIdleCleanup periodically triggers session idle cleanup.
func runIdleCleanup(ctx context.Context, mgr *session.Manager, logger *zap.Logger) {
	ticker := time.NewTicker(idleCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			archived := mgr.CleanupIdle(ctx)
			if archived > 0 {
				logger.Info("idle session cleanup", zap.Int("archived", archived))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Provider initialization
// ---------------------------------------------------------------------------

func initProvider(cfg config.LLMConfig, logger *zap.Logger) provider.Provider {
	provName := cfg.DefaultProvider
	if provName == "" {
		provName = "anthropic"
	}

	provCfg, ok := cfg.Providers[provName]
	if !ok {
		logger.Fatal("provider config not found",
			zap.String("provider", provName),
			zap.Any("available", mapKeys(cfg.Providers)),
		)
	}

	// All providers are routed through the Bifrost SDK.
	// Bifrost-specific overrides take precedence; fall back to provider config.
	bfProvider := mapBifrostProvider(cfg.Bifrost.Provider, provName)
	bfModel := cfg.Bifrost.Model
	if bfModel == "" {
		bfModel = provCfg.Model
	}
	bfAPIKey := cfg.Bifrost.APIKey
	if bfAPIKey == "" {
		bfAPIKey = provCfg.APIKey
	}
	bfBaseURL := cfg.Bifrost.BaseURL
	if bfBaseURL == "" {
		bfBaseURL = provCfg.BaseURL
	}

	adapter, err := bifrost.New(bifrost.Config{
		Provider:       bfProvider,
		Model:          bfModel,
		APIKey:         bfAPIKey,
		BaseURL:        bfBaseURL,
		FallbackModel:  cfg.Bifrost.FallbackModel,
		MaxConcurrency: cfg.Bifrost.MaxConcurrency,
		BufferSize:     cfg.Bifrost.BufferSize,
		ProxyURL:       cfg.ProxyURL,
		CustomHeaders:  cfg.CustomHeaders,
		Logger:         logger,
	})
	if err != nil {
		logger.Fatal("failed to create bifrost adapter", zap.Error(err))
	}
	logger.Info("bifrost provider initialized",
		zap.String("provider", string(bfProvider)),
		zap.String("model", bfModel),
		zap.String("fallback", cfg.Bifrost.FallbackModel),
		zap.Bool("proxy", cfg.ProxyURL != ""),
	)
	return adapter
}

// mapBifrostProvider converts a config string to a schemas.ModelProvider constant.
func mapBifrostProvider(override string, fallback string) schemas.ModelProvider {
	name := override
	if name == "" {
		name = fallback
	}
	switch name {
	case "anthropic":
		return schemas.Anthropic
	case "openai":
		return schemas.OpenAI
	default:
		// Use as-is for other providers (bedrock, vertex, etc.).
		return schemas.ModelProvider(name)
	}
}

func mapKeys(m map[string]config.ProviderConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
