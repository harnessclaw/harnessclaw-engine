// Package main is the entry point for the harnessclaw-engine service.
//
// Startup sequence:
//  1. Load configuration (Viper)
//  2. Initialize structured logger (Zap)
//  3. Initialize storage (SQLite)
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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/api"
	"harnessclaw-go/internal/artifact"
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
	sqlitesess "harnessclaw-go/internal/storage/sqlite"
	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/agenttool"
	"harnessclaw-go/internal/tool/artifacttool"
	"harnessclaw-go/internal/tool/askuserquestion"
	orchestratetool "harnessclaw-go/internal/tool/orchestrate"
	"harnessclaw-go/internal/tool/specialists"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/internal/tool/bash"
	"harnessclaw-go/internal/tool/fileedit"
	"harnessclaw-go/internal/tool/fileread"
	"harnessclaw-go/internal/tool/filewrite"
	"harnessclaw-go/internal/tool/glob"
	"harnessclaw-go/internal/tool/grep"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/internal/tool/tasktool"
	"harnessclaw-go/internal/tool/teamtool"
	"harnessclaw-go/internal/tool/webfetch"
	"harnessclaw-go/internal/tool/websearch"
	"harnessclaw-go/internal/tool/tavilysearch"
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
		zap.String("default_provider", cfg.LLM.DefaultProvider),
	)

	// --- Step 3: Initialize storage (always SQLite) ---
	store, err := sqlitesess.New(cfg.Session.DBPath)
	if err != nil {
		logger.Fatal("failed to initialize storage", zap.Error(err))
	}
	defer store.Close()
	logger.Info("storage initialized", zap.String("db_path", cfg.Session.DBPath))

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
		{cfg.Tools.WebFetch.Enabled, func() tool.Tool { return webfetch.New(cfg.Tools.WebFetch, logger) }},
		{cfg.Tools.WebSearch.Enabled, func() tool.Tool { return websearch.New(cfg.Tools.WebSearch, logger) }},
		{cfg.Tools.TavilySearch.Enabled, func() tool.Tool { return tavilysearch.New(cfg.Tools.TavilySearch, logger) }},
		// AskUserQuestion is L1's clarification mechanism. Always enabled
		// (no config — it's a passthrough to the WebSocket client).
		{true, func() tool.Tool { return askuserquestion.New(logger) }},
		// ArtifactWrite / ArtifactRead are the cross-agent shared-data
		// channel (doc §5). Always on — agents are expected to use them
		// instead of pasting large outputs back into the prompt.
		{true, func() tool.Tool { return artifacttool.NewWriteTool() }},
		{true, func() tool.Tool { return artifacttool.NewReadTool() }},
		// SubmitTaskResult is the L3 task-completion declaration
		// (doc §3 M3+M4). Always on; only fires when the dispatcher
		// supplied an ExpectedOutputs contract.
		{true, func() tool.Tool { return submittool.New() }},
	}
	for _, bt := range builtInTools {
		if bt.enabled {
			t := bt.factory()
			if err := registry.Register(t); err != nil {
				logger.Fatal("failed to register tool", zap.Error(err))
			}
			logger.Info("tool registered", zap.String("name", t.Name()), zap.Bool("is_enabled", t.IsEnabled()))
		}
	}

	// Load skills and register SkillTool.
	skillLoader := skill.NewLoader(cfg.Skills.Dirs, logger)
	loadResult := skillLoader.LoadAll()

	// Report skill loading results.
	logger.Info("skill loading summary",
		zap.Int("loaded", len(loadResult.Commands)),
		zap.Int("errors", len(loadResult.Errors)),
	)
	for _, e := range loadResult.Errors {
		logger.Error("skill load failed",
			zap.String("path", e.Path),
			zap.String("reason", e.Reason),
			zap.Error(e.Err),
		)
	}

	skillCommands := loadResult.Commands
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
	if err := registry.Register(skilltool.New(cmdRegistry, logger)); err != nil {
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

	// --- Step 7.5: Open artifact store + start TTL janitor ---
	// The store is the cross-agent shared-data channel (doc §3). Backed
	// by a dedicated SQLite file so the session DB and the artifact DB
	// can be tuned and rotated independently.
	artifactDBPath := defaultDBPath("artifacts.db")
	artifactStore, err := artifact.NewSQLiteStore(artifactDBPath, artifact.DefaultConfig())
	if err != nil {
		logger.Fatal("failed to initialize artifact store",
			zap.String("path", artifactDBPath),
			zap.Error(err),
		)
	}
	defer artifactStore.Close()
	logger.Info("artifact store initialized", zap.String("path", artifactDBPath))

	janitor := artifact.NewJanitor(artifactStore, 0 /*default 10m*/, logger)
	janitorCtx, janitorCancel := context.WithCancel(context.Background())
	defer janitorCancel()
	go janitor.Run(janitorCtx)

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

	// L2 (worker / sub-agent) settings live on QueryEngineConfig directly.
	// L1 settings (emma profile, restricted tool palette, small loop) are
	// applied by NewL1Engine below and overwrite the main-agent fields here.
	engCfg := engine.QueryEngineConfig{
		MaxTurns:             cfg.Engine.MaxTurns,
		AutoCompactThreshold: cfg.Engine.AutoCompactThreshold,
		ToolTimeout:          cfg.Engine.ToolTimeout,
		MaxTokens:            16384,
		SystemPrompt:         systemPrompt,
		// ClientTools comes from the WebSocket channel config since L1's
		// only delivery surface for client-routed tools (AskUserQuestion,
		// etc.) is the WebSocket. Forgetting this defaults Go's zero value
		// (false), which silently drops AskUserQuestion calls into the
		// server-side fallback — the warning users see in production.
		ClientTools: cfg.Channel.WebSocket.ClientTools,
		// MainAgentProfile / DisplayName / AllowedTools / MaxTurns are filled
		// in by NewL1Engine; setting non-default values here would be
		// overwritten anyway.
	}
	eng := engine.NewQueryEngine(llmProvider, registry, sessionMgr, compactor, permChecker, bus, logger, engCfg, cmdRegistry)
	logger.Info("engine initialized",
		zap.Int("max_turns", engCfg.MaxTurns),
		zap.Float64("compact_threshold", engCfg.AutoCompactThreshold),
	)

	// Wrap the QueryEngine in an L1Engine. From this point on, the channel
	// layer talks to `l1` (user-facing); Agent/Orchestrate tools continue to
	// use `eng` directly to spawn L2 sub-agents.
	l1 := engine.NewL1Engine(eng, engine.DefaultL1Config(), logger)
	logger.Info("L1 engine wrapped",
		zap.String("profile", l1.Config().Profile.Name),
		zap.String("display_name", l1.Config().DisplayName),
		zap.Strings("allowed_tools", l1.Config().AllowedTools),
		zap.Int("max_turns", l1.Config().MaxTurns),
	)

	// Register Task tool (post-engine: needs engine as AgentSpawner).
	// ToolPool is rebuilt per query loop, so late registration is safe.
	//
	// In the 3-tier architecture the Task tool (formerly "Agent") is not in
	// emma's tool palette (see L1Engine.AllowedTools). It is reachable from
	// L2 Specialists, which declares "Task" in its AgentDefinition.AllowedTools
	// and bypasses the AgentType blacklist (see internal/engine/subagent.go
	// filter logic).
	if err := registry.Register(agenttool.New(eng, logger)); err != nil {
		logger.Fatal("failed to register task tool", zap.Error(err))
	}
	logger.Info("task tool registered")

	// Register Specialists tool — the L1→L2 dispatch entry point. emma sees
	// this tool as her single delegation channel; Specialists itself spawns
	// L3 sub-agents internally via the Task tool above.
	if err := registry.Register(specialists.New(eng, logger)); err != nil {
		logger.Fatal("failed to register specialists tool", zap.Error(err))
	}
	logger.Info("specialists tool registered")

	// --- Step 8.5: Initialize multi-agent infrastructure ---
	agentReg := agent.NewAgentRegistry()
	broker := agent.NewMessageBroker()
	teamMgr := agent.NewTeamManager()

	// Initialize task store — prefer SQLite for persistence, fall back to memory.
	var taskStore task.Store
	taskDBPath := defaultDBPath("tasks.db")
	sqliteStore, err := task.NewSQLiteStore(taskDBPath)
	if err != nil {
		logger.Warn("failed to open SQLite task store, falling back to memory",
			zap.String("path", taskDBPath),
			zap.Error(err),
		)
		taskStore = task.NewMemoryStore()
	} else {
		taskStore = sqliteStore
		defer sqliteStore.Close()
		logger.Info("task store initialized", zap.String("backend", "sqlite"), zap.String("path", taskDBPath))
	}

	agentDefReg := agent.NewAgentDefinitionRegistry()

	// Initialize agent definition store (SQLite) and service.
	agentDefDBPath := defaultDBPath("agent_definitions.db")
	agentDefStore, err := agent.NewSQLiteAgentStore(agentDefDBPath)
	if err != nil {
		logger.Fatal("failed to initialize agent definition store", zap.Error(err))
	}
	defer agentDefStore.Close()
	logger.Info("agent definition store initialized", zap.String("path", agentDefDBPath))

	agentSvc := agent.NewAgentService(agentDefStore, agentDefReg, bus, logger)

	// Sync built-in agent definitions to SQLite.
	if err := agentSvc.SyncBuiltins(context.Background()); err != nil {
		logger.Warn("failed to sync builtin agent definitions", zap.Error(err))
	}

	// Load all persisted definitions from SQLite into in-memory registry.
	// YAML is no longer auto-scanned; use POST /console/v1/agents/import to import.
	if err := agentSvc.LoadAllToRegistry(context.Background()); err != nil {
		logger.Warn("failed to load agent definitions to registry", zap.Error(err))
	}

	// Log all registered agent definitions.
	for _, def := range agentDefReg.All() {
		fields := []zap.Field{
			zap.String("name", def.Name),
			zap.String("agent_type", string(def.AgentType)),
			zap.String("profile", def.Profile),
		}
		if def.DisplayName != "" {
			fields = append(fields, zap.String("display_name", def.DisplayName))
		}
		if def.Source != "" {
			fields = append(fields, zap.String("source", def.Source))
		}
		if len(def.AllowedTools) > 0 {
			fields = append(fields, zap.Int("allowed_tools", len(def.AllowedTools)))
		}
		if len(def.Skills) > 0 {
			fields = append(fields, zap.Strings("skills", def.Skills))
		}
		if def.AutoTeam {
			fields = append(fields, zap.Int("sub_agents", len(def.SubAgents)))
		}
		logger.Info("agent definition registered", fields...)
	}
	logger.Info("agent definitions summary", zap.Int("total", len(agentDefReg.All())))

	// Register Orchestrate tool (Phase-2 multi-step coordinator).
	// The roster combines built-in profile names with all loaded agent
	// definitions; it is queried per Execute() call so newly-registered
	// definitions are picked up automatically.
	orchestrateRoster := &agentDefRoster{reg: agentDefReg}
	if err := registry.Register(orchestratetool.New(eng, orchestrateRoster, logger)); err != nil {
		logger.Fatal("failed to register orchestrate tool", zap.Error(err))
	}
	logger.Info("orchestrate tool registered")

	// Inject multi-agent dependencies into the engine.
	eng.SetAgentRegistry(agentReg)
	eng.SetMessageBroker(broker)
	eng.SetDefRegistry(agentDefReg)
	eng.SetArtifactStore(artifactStore)

	// Register task tools (scoped to a default scope for now).
	defaultScope := "default"
	taskTools := []tool.Tool{
		tasktool.NewCreate(taskStore, defaultScope),
		tasktool.NewGet(taskStore, defaultScope),
		tasktool.NewList(taskStore, defaultScope),
		tasktool.NewUpdate(taskStore, defaultScope),
	}
	for _, tt := range taskTools {
		if err := registry.Register(tt); err != nil {
			logger.Fatal("failed to register task tool", zap.Error(err))
		}
	}
	logger.Info("task tools registered", zap.Int("count", len(taskTools)))

	// Register team tools.
	if err := registry.Register(teamtool.NewCreate(teamMgr, broker, logger)); err != nil {
		logger.Fatal("failed to register team create tool", zap.Error(err))
	}
	if err := registry.Register(teamtool.NewDelete(teamMgr, logger)); err != nil {
		logger.Fatal("failed to register team delete tool", zap.Error(err))
	}
	logger.Info("team tools registered")

	logger.Info("multi-agent infrastructure initialized",
		zap.Int("agent_definitions", len(agentDefReg.All())),
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

	// The router talks to L1 — that is the only user-facing engine.
	// L2 sub-agents are reached only indirectly via Agent/Orchestrate tools.
	rtr := router.New(l1, channels, middlewares, logger)
	logger.Info("router initialized",
		zap.Int("middleware_count", len(middlewares)),
		zap.Int("channel_count", len(channels)),
	)

	// --- Step 10: Start channels ---
	channelCtx, channelCancel := context.WithCancel(context.Background())
	defer channelCancel()
	channelErrCh := make(chan error, len(channels))
	for name, ch := range channels {
		go func(n string, c channel.Channel) {
			if err := c.Start(channelCtx, rtr.Handle); err != nil {
				logger.Error("channel exited with error", zap.String("channel", n), zap.Error(err))
				channelErrCh <- fmt.Errorf("channel %s: %w", n, err)
			}
		}(name, ch)
	}
	// TODO: Start HTTP and Feishu channels when implementations are ready.

	// --- Step 10.5: Start Console management API server ---
	var consoleServer *api.Server
	if cfg.Console.Enabled {
		consoleServer = api.NewServer(api.ServerConfig{
			Host: cfg.Console.Host,
			Port: cfg.Console.Port,
		}, agentSvc, logger)
		go func() {
			if err := consoleServer.Start(); err != nil {
				logger.Error("console API server exited", zap.Error(err))
			}
		}()
	}

	logger.Info("all channels started, service is ready")

	// --- Step 11: Wait for shutdown signal or channel failure ---
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		// First signal: begin graceful shutdown.
		logger.Info("shutdown signal received, stopping gracefully...")
	case err := <-channelErrCh:
		// Channel failed (e.g. port in use): shut down immediately.
		logger.Fatal("channel startup failed, exiting", zap.Error(err))
	}

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

	// Stop Console API server.
	if consoleServer != nil {
		if err := consoleServer.Stop(shutdownCtx); err != nil {
			logger.Error("console API server stop error", zap.Error(err))
		}
		logger.Info("console API server stopped")
	}

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

// defaultDBPath returns ~/.harnessclaw/db/<name> with cross-platform home dir resolution.
func defaultDBPath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home cannot be determined.
		return filepath.Join(".harnessclaw", "db", name)
	}
	return filepath.Join(home, ".harnessclaw", "db", name)
}

// agentDefRoster adapts the agent definition registry to the Orchestrate
// tool's AgentRoster interface. It also includes built-in profile names so
// the Planner can route to non-team profiles like Explore/Plan/general-purpose.
type agentDefRoster struct {
	reg *agent.AgentDefinitionRegistry
}

// builtInRosterAgents are the always-available profile names the Planner
// may target, in addition to agent definitions registered at runtime.
var builtInRosterAgents = []string{
	"general-purpose",
	"Explore",
	"Plan",
	"worker",
}

func (r *agentDefRoster) AvailableSubagentTypes() []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 16)
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, n := range builtInRosterAgents {
		add(n)
	}
	if r.reg != nil {
		for _, name := range r.reg.Names() {
			add(name)
		}
	}
	return out
}
