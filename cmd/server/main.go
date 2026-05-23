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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/api"
	"harnessclaw-go/internal/api/agentcapabilities"
	"harnessclaw-go/internal/api/modelsregistry"
	"harnessclaw-go/internal/api/sessionmetrics"
	"harnessclaw-go/internal/channel"
	wsch "harnessclaw-go/internal/channel/websocket"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompter"
	"harnessclaw-go/internal/engine/resume"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/api/providersmgmt"
	"harnessclaw-go/internal/api/toolsmgmt"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/bifrost"
	"harnessclaw-go/internal/provider/failover"
	"harnessclaw-go/internal/provider/manager"
	modelregistry "harnessclaw-go/internal/provider/registry"
	providerstats "harnessclaw-go/internal/provider/stats"
	"harnessclaw-go/internal/router"
	"harnessclaw-go/internal/router/middleware"
	"harnessclaw-go/internal/skill"
	sqlitesess "harnessclaw-go/internal/storage/sqlite"
	"harnessclaw-go/internal/task"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/internal/tool/agenttool"
	"harnessclaw-go/internal/tool/askuserquestion"
	orchestratetool "harnessclaw-go/internal/tool/orchestrate"
	"harnessclaw-go/internal/tool/scheduler"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/internal/tool/bash"
	"harnessclaw-go/internal/tool/fileedit"
	"harnessclaw-go/internal/tool/fileread"
	"harnessclaw-go/internal/tool/filewrite"
	"harnessclaw-go/internal/tool/glob"
	"harnessclaw-go/internal/tool/grep"
	"harnessclaw-go/internal/tool/listloadedskills"
	"harnessclaw-go/internal/tool/loadskill"
	"harnessclaw-go/internal/tool/metatool"
	"harnessclaw-go/internal/tool/plantool"
	"harnessclaw-go/internal/tool/promotetool"
	"harnessclaw-go/internal/tool/searchskill"
	"harnessclaw-go/internal/tool/skilltool"
	"harnessclaw-go/internal/tool/unloadskill"
	"harnessclaw-go/internal/tool/tasktool"
	"harnessclaw-go/internal/tool/teamtool"
	"harnessclaw-go/internal/tool/webfetch"
	"harnessclaw-go/internal/tool/websearch"
	"harnessclaw-go/internal/tool/tavilysearch"
	"harnessclaw-go/internal/workspace"
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

	// Drop malformed LLM content (bad providers / endpoints / chain
	// entries) with WARN logs. Hard config errors are already caught
	// by Validate above; this pass only removes the parts that
	// won't load cleanly, leaving the rest usable.
	cfg.SanitizeLLM(logger)

	// Sanity-check every endpoint's model_type. Unknown tokens are
	// dropped with a warn so a typo doesn't fail engine startup — the
	// gate quietly degrades to manifest fallback for that endpoint
	// until the user fixes the config via the PATCH API or yaml edit.
	for pName, prov := range cfg.LLM.Providers {
		for epName, ep := range prov.Endpoints {
			if len(ep.ModelType) == 0 {
				continue
			}
			known, unknown := modelregistry.FilterKnownTokens(ep.ModelType)
			if len(unknown) > 0 {
				logger.Warn("dropping unknown model_type tokens",
					zap.String("provider", pName),
					zap.String("endpoint", epName),
					zap.Strings("unknown", unknown),
					zap.Strings("kept", known),
				)
				ep.ModelType = known
				prov.Endpoints[epName] = ep
			}
		}
		cfg.LLM.Providers[pName] = prov
	}

	logger.Info("starting harnessclaw-engine",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.Int("provider_count", len(cfg.LLM.Providers)),
		zap.String("agent_primary", cfg.Agent.Primary),
		zap.Strings("agent_fallback_chain", cfg.Agent.FallbackChain),
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

	// workspaceRootDir is the shared root for plan.json / tasks/ /
	// deliverables/ across every L1 session. Defaults to
	// ~/.harnessclaw/workspace — the same convention skills and the
	// session DB already use. Empty when UserHomeDir fails (e.g.
	// containerised builds with no $HOME) which disables the
	// PlanUpdate/MetaWrite/Promote tools at registry time.
	var workspaceRootDir string
	if home, err := os.UserHomeDir(); err == nil {
		workspaceRootDir = filepath.Join(home, ".harnessclaw", "workspace")
	}

	// planWriterReg is the per-process registry of single-consumer plan.json
	// writers. PlanUpdate / Promote / the engine's post-spawn reconciler
	// all share it so every mutation for a given session funnels through
	// one goroutine (D11). The default (lazy-initialised) registry anchors
	// on workspace.DefaultRootDir() — same path we computed locally above —
	// so calling DefaultPlanWriterRegistry here also seeds it for the
	// engine call sites without a second registry being created later.
	planWriterReg := workspace.DefaultPlanWriterRegistry()

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
		// WebSearch / TavilySearch are always registered so the toolsmgmt
		// PATCH API can hot-enable them without a restart. Their IsEnabled()
		// returns false when disabled or under-credentialed, so the pool's
		// EnabledTools() filter keeps them invisible to the LLM until the
		// API flips them on with full credentials.
		{true, func() tool.Tool { return websearch.New(cfg.Tools.WebSearch, logger) }},
		{true, func() tool.Tool { return tavilysearch.New(cfg.Tools.TavilySearch, logger) }},
		// AskUserQuestion is L1's clarification mechanism. Always enabled
		// (no config — it's a passthrough to the WebSocket client).
		{true, func() tool.Tool { return askuserquestion.New(logger) }},
		// SubmitTaskResult is the L3 task-completion declaration
		// (doc §3 M3+M4). Always on; only fires when the dispatcher
		// supplied an ExpectedOutputs contract.
		{true, func() tool.Tool { return submittool.New() }},
		// EscalateToPlanner is the L3 needs-planning escape hatch.
		// Pairs with SubmitTaskResult: every TierSubAgent worker must
		// reach exactly one of the two before its loop terminates.
		{true, func() tool.Tool { return submittool.NewEscalate() }},
		// PlanUpdate / MetaWrite / Promote are the local-files-as-truth
		// trio (doc §3). Only registered when the workspace root resolved
		// so unit-tests and headless builds without a home dir stay
		// unchanged. Promote reads its event channel from ctx, so no
		// per-session wiring is needed here.
		{workspaceRootDir != "", func() tool.Tool { return plantool.NewPlanUpdateTool(planWriterReg, workspaceRootDir) }},
		{workspaceRootDir != "", func() tool.Tool { return metatool.NewMetaWriteTool(workspaceRootDir) }},
		{workspaceRootDir != "", func() tool.Tool { return promotetool.NewPromoteTool(planWriterReg, workspaceRootDir, nil) }},
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

	// Build a runtime skill Reader (independent of startup Loader) so
	// SearchSkill / LoadSkill / freelancer hydration can pick up newly
	// downloaded skills without restarting the server.
	skillReader := skill.NewReader(cfg.Skills.Dirs, logger)
	if err := registry.Register(searchskill.New(skillReader, logger)); err != nil {
		logger.Fatal("failed to register SearchSkill tool", zap.Error(err))
	}
	if err := registry.Register(loadskill.New(skillReader, logger)); err != nil {
		logger.Fatal("failed to register LoadSkill tool", zap.Error(err))
	}
	if err := registry.Register(unloadskill.New(logger)); err != nil {
		logger.Fatal("failed to register UnloadSkill tool", zap.Error(err))
	}
	if err := registry.Register(listloadedskills.New(logger)); err != nil {
		logger.Fatal("failed to register ListLoadedSkills tool", zap.Error(err))
	}

	logger.Info("tool registry initialized",
		zap.Int("tool_count", len(registry.All())),
		zap.Int("skill_count", len(skillCommands)),
	)

	// --- Step 6: Initialize LLM provider ---
	// Load the embedded model + provider registry. The default manifest
	// ships in the binary; the registry tells the bifrost adapter which
	// quirks to apply for the configured provider, and serves the same
	// data over /api/v1/models for the client capability gate.
	regManifest, err := modelregistry.DefaultManifest()
	if err != nil {
		logger.Fatal("load default model manifest", zap.Error(err))
	}
	modelReg := modelregistry.NewRegistry(regManifest)

	llmProvider, providerMgr := initProvider(cfg.LLM, cfg.Agent, cfg.SourcePath, modelReg, logger)

	// Session-metrics registry: a single in-process registry holds the
	// per-session Tracker (cumulative LLM/tool/sub-agent counters). The
	// StatsProvider decorator increments LLM call stats on every
	// GenerateOnce return; the manager binds the registry so each new /
	// reloaded session installs its Tracker; the engine + executor read
	// from the same registry to attribute tool / sub-agent activity.
	statsRegistry := sessionstats.NewRegistry()
	llmProvider = providerstats.New(llmProvider, statsRegistry)
	logger.Info("LLM provider initialized", zap.String("provider", llmProvider.Name()))

	// --- Step 7: Create session manager ---
	sessionMgr := session.NewManager(store, logger, cfg.Session.IdleTimeout)
	sessionMgr.BindStatsRegistry(statsRegistry)
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

	// L2 (worker / sub-agent) settings live on QueryEngineConfig directly.
	// L1 settings (emma profile, restricted tool palette, small loop) are
	// applied by NewL1Engine below and overwrite the main-agent fields here.
	engCfg := engine.QueryEngineConfig{
		MaxTurns:             cfg.Agent.MaxTurns,
		AutoCompactThreshold: cfg.Engine.AutoCompactThreshold,
		ToolTimeout:          cfg.Engine.ToolTimeout,
		// MaxTokens is the per-response cap forwarded as
		// ChatRequest.MaxTokens; the bifrost adapter's
		// agent/endpoint-resolved default kicks in when this is 0.
		MaxTokens: cfg.Agent.MaxTokens,
		// ContextWindow is capped against the primary endpoint's intrinsic
		// limit by Manager.EffectiveContextWindow() — agent.context_window=500k
		// against an endpoint.context_window=200k → engine compactor uses 200k.
		// Reported in startup log + GET /api/v1/agent so operators can confirm.
		ContextWindow: providerMgr.EffectiveContextWindow(),
		SystemPrompt:  systemPrompt,
		// ClientTools comes from the WebSocket channel config since L1's
		// only delivery surface for client-routed tools (AskUserQuestion,
		// etc.) is the WebSocket. Forgetting this defaults Go's zero value
		// (false), which silently drops AskUserQuestion calls into the
		// server-side fallback — the warning users see in production.
		ClientTools:         cfg.Channel.WebSocket.ClientTools,
		MaxPlanReplans:      cfg.Engine.MaxPlanReplans,
		MaxStepAttempts:     cfg.Engine.MaxStepAttempts,
		LLMMaxRetries:       cfg.LLM.MaxRetries,
		LLMAPITimeout:       cfg.LLM.APITimeout,
		LLMFirstByteTimeout: cfg.LLM.FirstByteTimeout,
		// MainAgentProfile / DisplayName / AllowedTools / MaxTurns are filled
		// in by NewL1Engine; setting non-default values here would be
		// overwritten anyway.
	}
	eng := engine.NewQueryEngine(llmProvider, registry, sessionMgr, compactor, permChecker, bus, logger, engCfg, cmdRegistry)
	logger.Info("engine initialized",
		zap.Int("max_turns", engCfg.MaxTurns),
		zap.Float64("compact_threshold", engCfg.AutoCompactThreshold),
		zap.Int("agent_context_window", cfg.Agent.ContextWindow),
		zap.Int("effective_context_window", engCfg.ContextWindow),
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
	// In the 3-tier architecture the task tool (formerly "Agent") is not in
	// emma's tool palette (see L1Engine.AllowedTools). It is reachable from
	// the L2 scheduler, which declares "task" in its AgentDefinition.AllowedTools
	// and bypasses the AgentType blacklist (see internal/engine/subagent.go
	// filter logic).
	if err := registry.Register(agenttool.New(eng, logger)); err != nil {
		logger.Fatal("failed to register task tool", zap.Error(err))
	}
	logger.Info("task tool registered")

	// Register scheduler tool — the L1→L2 dispatch entry point. emma sees
	// this tool as her single delegation channel; the scheduler itself spawns
	// L3 sub-agents internally via the task tool above.
	if err := registry.Register(scheduler.New(eng, logger)); err != nil {
		logger.Fatal("failed to register scheduler tool", zap.Error(err))
	}
	logger.Info("scheduler tool registered")

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
	// Session-metrics wiring. The engine reads from the same registry the
	// StatsProvider writes into; the session manager hand-off lets the
	// trace-end flush worker force-persist the snapshot on lifecycle
	// transitions instead of waiting for the debounce.
	eng.SetStatsRegistry(statsRegistry)
	eng.SetSessionManager(sessionMgr)
	eng.SetSkillReader(skillReader)

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

	// The router talks to L1 — that is the only user-facing engine.
	// L2 sub-agents are reached only indirectly via Agent/Orchestrate tools.
	// modelInfoBridge adapts provider.Manager + registry into the
	// router's ModelInfoProvider so the multimodal Gate can reject
	// image/PDF inputs against a text-only active model. Nil-tolerant:
	// if either dependency is missing the gate degrades to "trust the
	// adapter" rather than crashing.
	var modelInfo router.ModelInfoProvider
	if providerMgr != nil && modelReg != nil {
		modelInfo = &routerModelInfoBridge{mgr: providerMgr, reg: modelReg}
	}
	rtr := router.New(l1, channels, middlewares, modelInfo, logger)

	// Register WebSocket channel if enabled.
	//
	// Recovery wiring: a WaitStore mounted on the same SQLite DB as the
	// session store; a Prompter that persists outstanding prompts; and
	// a TextResumer that re-enters the engine via rtr.Handle when a
	// reply arrives post-restart. The channel is told about all three
	// before Start so the upgrade path can advertise the recovery
	// capability and replay unanswered prompts to reconnecting clients.
	if cfg.Channel.WebSocket.Enabled {
		waitStore, err := sqlitesess.NewWaitStore(store.DB())
		if err != nil {
			logger.Fatal("failed to initialise wait store", zap.Error(err))
		}
		waitPrompter := prompter.New(prompter.Config{Store: waitStore})

		wsCh := wsch.New(cfg.Channel.WebSocket, nil, logger)
		wsCh.SetPrompter(waitPrompter)
		wsCh.SetResumer(resume.New(rtr.Handle, logger))
		wsCh.GetTranslator().SetIssuer(waitPrompter)

		// Periodic janitor: every hour, sweep waits that have passed
		// their TTL (15d default) so abandoned conversations don't
		// accumulate forever. Hourly cadence is fine — a wait may
		// linger up to 60 min past nominal expiry, which matters for
		// nothing in practice given a 15-day TTL.
		janitorCtx, janitorCancel := context.WithCancel(context.Background())
		defer janitorCancel()
		go runWaitJanitor(janitorCtx, waitPrompter, logger)

		channels["websocket"] = wsCh
		logger.Info("websocket channel registered",
			zap.String("addr", fmt.Sprintf("%s:%d", cfg.Channel.WebSocket.Host, cfg.Channel.WebSocket.Port)),
			zap.String("path", cfg.Channel.WebSocket.Path),
			zap.Bool("recovery_enabled", true),
		)
	}
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
	// Session metrics dashboard is served at /api/v1/sessions/{id}/metrics
	// on the same port as Console management so the front-end has a
	// single management endpoint to consult. The handler prefers the
	// live in-memory tracker and falls back to the persisted snapshot
	// via `store`.
	metricsHandler := sessionmetrics.New(statsRegistry, store, logger)
	modelsHandler := modelsregistry.New(modelReg, logger)

	// providersHandler is non-nil only when running in multi-provider
	// mode (providerMgr != nil). Single-provider deployments don't
	// mount the runtime providers API because there's nothing to
	// hot-swap and persisting an empty fallback_chain would be
	// surprising.
	//
	// cfg.SourcePath is the absolute path of the yaml viper actually
	// loaded — whether the operator passed -config explicitly or
	// viper auto-discovered one in ./configs/ or .. So mutations
	// always write back to the same file the server started with.
	var providersHandler http.Handler
	if providerMgr != nil {
		providersHandler = providersmgmt.New(providerMgr, cfg.SourcePath, logger)
		logger.Info("providersmgmt API mounted",
			zap.String("config_source", cfg.SourcePath),
		)
	}
	toolsHandler := toolsmgmt.New(registry, cfg, cfg.SourcePath, logger)
	logger.Info("toolsmgmt API mounted",
		zap.String("config_source", cfg.SourcePath),
	)

	// Agent capabilities endpoint: serves the same SupportsFlags the
	// router gate uses by reusing the bridge instance directly, so the
	// client never disagrees with the server about what's allowed.
	var capabilitiesHandler http.Handler
	if modelInfo != nil {
		capabilitiesHandler = agentcapabilities.New(modelInfo, logger)
		logger.Info("agent capabilities API mounted")
	}

	var consoleServer *api.Server
	if cfg.Console.Enabled {
		// Artifact HTTP endpoint was removed with the artifact package.
		// Promoted files live on disk under {workspace}/session/{sid}/
		// deliverables/, which the client opens directly.
		consoleServer = api.NewServer(api.ServerConfig{
			Host: cfg.Console.Host,
			Port: cfg.Console.Port,
		}, agentSvc, metricsHandler, modelsHandler, providersHandler, toolsHandler, nil, capabilitiesHandler, logger)
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

	// Stop the per-session PlanWriter goroutines so any final mutations
	// drain to disk before the process exits. StopAll respects ctx so a
	// stuck writer cannot block shutdown indefinitely.
	planWriterReg.StopAll(shutdownCtx)
	logger.Info("plan writer registry stopped")

	// Flush logger.
	logger.Info("shutdown complete")
	_ = logger.Sync()
}

// initLogger creates a Zap logger from config. When output=file, the
// parent directory is created on demand (mkdir -p) and the path may
// start with "~/" to refer to the user's home directory — saves
// operators from getting a useless "no such file or directory" before
// the logger even exists.
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
		resolved, err := resolveLogPath(cfg.FilePath)
		if err != nil {
			return nil, fmt.Errorf("log file_path %q: %w", cfg.FilePath, err)
		}
		// mkdir -p the parent so zap.Build() doesn't fail with
		// "no such file or directory". Permission failures here usually
		// mean the user picked a path under /var/log or similar
		// system-owned dirs without sudo — surface a clear hint.
		if dir := filepath.Dir(resolved); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create log dir %q (try a path under your home, e.g. ~/.harnessclaw/log/server.log): %w", dir, err)
			}
		}
		zapCfg.OutputPaths = []string{resolved}
	}

	return zapCfg.Build()
}

// resolveLogPath expands a leading "~/" to the user's home directory.
// Bare "~" (no slash) is left alone — that path is probably intentional.
// On the rare case where os.UserHomeDir fails (some sandbox envs), the
// original path is returned and the caller handles the open error.
func resolveLogPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
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

// runWaitJanitor periodically deletes pending_waits rows whose
// ExpiresAt has passed. Without this the table accumulates one row
// per abandoned conversation forever — a user who closes their browser
// mid-prompt never returns to delete the wait, so the TTL sweep is the
// only ground truth.
//
// Frequency is intentionally modest (1 hour): with a 15-day TTL a
// hour of slack past nominal expiry is irrelevant, and hourly DELETE
// is cheap on the single-writer SQLite (one indexed range delete).
func runWaitJanitor(ctx context.Context, p *prompter.Prompter, logger *zap.Logger) {
	const interval = time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := p.SweepExpired(ctx)
			if err != nil {
				logger.Warn("wait janitor sweep failed", zap.Error(err))
				continue
			}
			if n > 0 {
				logger.Info("wait janitor reclaimed expired prompts", zap.Int("count", n))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Provider initialization
// ---------------------------------------------------------------------------

// initProvider builds the engine-facing provider.Provider.
//
// Always goes through manager.Manager so the providersmgmt API is
// available regardless of chain length. Manager wraps a hot-swappable
// Failover dispatcher and implements provider.Provider so the engine
// sees no change. Empty effective chain (no primary, no fallback)
// enters degraded mode — Chat returns ErrNoEndpoint until the
// operator populates the chain via PATCH /api/v1/agent.
func initProvider(cfg config.LLMConfig, agent config.AgentConfig, sourcePath string, modelReg *modelregistry.Registry, logger *zap.Logger) (provider.Provider, *manager.Manager) {
	chain := effectiveChain(agent)

	if len(chain) == 0 {
		logger.Warn("agent.primary and agent.fallback_chain are both empty — server starting in degraded mode; LLM requests will fail until the chain is populated",
			zap.String("config_source", sourcePath),
			zap.Any("providers_declared", mapKeys(cfg.Providers)),
			zap.String("how_to_fix", "PATCH /api/v1/agent {\"primary\":\"provider:endpoint\"}"),
		)
	}
	adapterBuilder := func(provName string, provCfg config.ProviderConfig, epName string, epCfg config.EndpointConfig, agent config.AgentConfig) (*bifrost.Adapter, error) {
		if epCfg.MaxTokens == 0 {
			epCfg.MaxTokens = defaultMaxTokens(cfg)
		}
		return buildBifrostAdapter(provName, provCfg, epName, epCfg, agent, cfg, modelReg, logger)
	}
	policyBuilder := func(h config.ProviderHealthConfig) (failover.RetryPolicy, failover.RetryPolicy, failover.RetryPolicy) {
		return failoverPolicyFromBudget("fast", h.PrimaryBudget, failover.FastPolicy),
			failoverPolicyFromBudget("medium", h.LastHealthyBudget, failover.MediumPolicy),
			failoverPolicyFromBudget("probe", h.ProbeBudget, failover.ProbePolicy)
	}
	mgr, err := manager.New(cfg, agent, modelReg, adapterBuilder, policyBuilder, logger)
	if err != nil {
		logger.Fatal("failed to build provider manager", zap.Error(err))
	}
	logger.Info("LLM provider manager ready (hot-swap enabled)",
		zap.String("primary", agent.Primary),
		zap.Strings("fallback_chain", agent.FallbackChain),
		zap.Duration("cooldown_base", cfg.Health.CooldownBase),
		zap.Duration("cooldown_max", cfg.Health.CooldownMax),
		zap.Int("cooldown_factor", cfg.Health.CooldownFactor),
		zap.Duration("primary_budget", cfg.Health.PrimaryBudget),
		zap.Duration("last_healthy_budget", cfg.Health.LastHealthyBudget),
		zap.Duration("probe_budget", cfg.Health.ProbeBudget),
	)
	return mgr, mgr
}

// effectiveChain returns [primary, ...fallback_chain] deduplicated —
// the actual order the dispatcher will try endpoints. Mirrors
// manager.effectiveChain (kept here to avoid a build dependency from
// logging code into manager).
func effectiveChain(a config.AgentConfig) []string {
	out := make([]string, 0, 1+len(a.FallbackChain))
	seen := map[string]bool{}
	if a.Primary != "" {
		out = append(out, a.Primary)
		seen[a.Primary] = true
	}
	for _, e := range a.FallbackChain {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// buildBifrostAdapter constructs one Bifrost adapter for a
// (provider, endpoint) pair. The provider supplies credentials
// (type / base_url / api_key); the endpoint supplies the model and
// per-call tuning (max_tokens / temperature / enable_thinking); the
// agent supplies app-level defaults that override the endpoint's own
// values when present, after range adjustment (temperature) and
// capping (max_tokens). See resolveDefaults for the rules.
func buildBifrostAdapter(
	provName string,
	provCfg config.ProviderConfig,
	epName string,
	epCfg config.EndpointConfig,
	agent config.AgentConfig,
	cfg config.LLMConfig,
	modelReg *modelregistry.Registry,
	logger *zap.Logger,
) (*bifrost.Adapter, error) {
	bfProvider, ok := bifrost.ProviderTypeOf(provCfg.Type)
	if !ok {
		return nil, fmt.Errorf("provider %q: unknown type %q (allowed: %v)",
			provName, provCfg.Type, bifrost.AllowedTypeNames())
	}

	// Quirks: prefer the provider YAML key (e.g. "deepseek") so vendors
	// running on an openai-compatible backend get their own quirks
	// (deepseek_type thinking style) instead of the generic openai ones.
	// Fall back to the backend type when no named entry exists.
	var quirks *bifrost.ProviderQuirks
	provSpec := modelReg.LookupProvider(provName)
	if provSpec == nil {
		provSpec = modelReg.LookupProvider(provCfg.Type)
	}
	if provSpec != nil {
		quirks = &bifrost.ProviderQuirks{
			ThinkingParamStyle:             provSpec.Quirks.ThinkingParamStyle,
			ToolCallsRequireReasoningField: provSpec.Quirks.ToolCallsRequireReasoningField,
			ExtraParamsPassthroughRequired: provSpec.Quirks.ExtraParamsPassthroughRequired,
			InlineUsageOnEveryChunk:        provSpec.Quirks.InlineUsageOnEveryChunk,
			ExplicitCacheControl:           provSpec.Quirks.ExplicitCacheControl,
		}
	}

	effectiveTemp := resolveEffectiveTemperature(provCfg.Type, agent.Temperature, epCfg.Temperature)
	effectiveMax := resolveEffectiveMaxTokens(agent.MaxTokens, epCfg.MaxTokens)

	adapter, err := bifrost.New(bifrost.Config{
		Provider:           bfProvider,
		Model:              epCfg.Model,
		APIKey:             provCfg.APIKey,
		BaseURL:            provCfg.BaseURL,
		MaxConcurrency:     cfg.Bifrost.MaxConcurrency,
		BufferSize:         cfg.Bifrost.BufferSize,
		ProxyURL:           cfg.ProxyURL,
		CustomHeaders:      cfg.CustomHeaders,
		EnableThinking:     epCfg.EnableThinking,
		Quirks:             quirks,
		DefaultTemperature: effectiveTemp,
		DefaultMaxTokens:   effectiveMax,
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	thinkingState := "default"
	if epCfg.EnableThinking != nil {
		if *epCfg.EnableThinking {
			thinkingState = "enabled"
		} else {
			thinkingState = "disabled"
		}
	}
	logger.Info("bifrost adapter built",
		zap.String("provider", provName),
		zap.String("endpoint", epName),
		zap.String("type", provCfg.Type),
		zap.String("backend", string(bfProvider)),
		zap.String("model", epCfg.Model),
		zap.Int("max_tokens", epCfg.MaxTokens),
		zap.Int("effective_max_tokens", effectiveMax),
		zap.Float64("effective_temperature", effectiveTemp),
		zap.Bool("proxy", cfg.ProxyURL != ""),
		zap.String("thinking", thinkingState),
	)
	return adapter, nil
}

// resolveEffectiveTemperature picks the temperature baked into the
// adapter as its default when ChatRequest.Temperature is 0.
//
// Convention: agent.temperature is authored on a unified [0, 1] scale
// (so config / API consumers don't need to know per-vendor ranges).
// The framework scales it by provider type to the vendor's legal range:
//
//   - anthropic  → [0, 1]  scale factor 1.0  (no change)
//   - openai     → [0, 2]  scale factor 2.0
//   - gemini     → [0, 2]  scale factor 2.0
//
// When agent.temperature is 0 (unset), the endpoint's own Temperature
// (assumed already in the vendor's native range) is used verbatim.
// Unknown provider types pass through unscaled.
func resolveEffectiveTemperature(providerType string, agentTemp, epTemp float64) float64 {
	if agentTemp <= 0 {
		return epTemp
	}
	switch providerType {
	case "openai", "gemini":
		return agentTemp * 2.0
	case "anthropic":
		return agentTemp
	default:
		return agentTemp
	}
}

// resolveEffectiveMaxTokens picks the max_tokens baked into the
// adapter as its default when ChatRequest.MaxTokens is 0.
//
// Rule: agent.max_tokens wins when set AND ≤ endpoint.max_tokens
// (endpoint acts as the hard ceiling — operator-configured per-model
// cap is never exceeded). Otherwise endpoint.max_tokens is used.
func resolveEffectiveMaxTokens(agentMax, epMax int) int {
	if agentMax <= 0 {
		return epMax
	}
	if epMax > 0 && agentMax > epMax {
		return epMax
	}
	return agentMax
}

// defaultMaxTokens returns the configured default or 8192 fallback.
func defaultMaxTokens(cfg config.LLMConfig) int {
	if cfg.DefaultMaxTokens > 0 {
		return cfg.DefaultMaxTokens
	}
	return 8192
}

// failoverPolicyFromBudget converts a config Duration into a
// failover.RetryPolicy with the supplied name. Zero duration falls
// back to the package default policy.
func failoverPolicyFromBudget(name string, budget time.Duration, fallback failover.RetryPolicy) failover.RetryPolicy {
	if budget <= 0 {
		return fallback
	}
	return failover.RetryPolicy{Name: name, Budget: budget}
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

func (r *agentDefRoster) ListForPlanner() []agent.PlannerListing {
	if r.reg == nil {
		return nil
	}
	return r.reg.ListForPlanner()
}

// routerModelInfoBridge adapts provider.Manager + model registry to the
// router's ModelInfoProvider interface. ActiveSupports looks up the
// primary endpoint's manifest entry to surface its capability matrix
// for the multimodal Gate. Returns a zero SupportsFlags when the
// active model isn't found in the registry — the gate then rejects
// non-text content, which is the safe default for unmapped models.
type routerModelInfoBridge struct {
	mgr *manager.Manager
	reg *modelregistry.Registry
}

func (b *routerModelInfoBridge) ActiveModelKey() string {
	return b.mgr.ActiveModelKey()
}

func (b *routerModelInfoBridge) ActiveSupports() modelregistry.SupportsFlags {
	// Intersection across the entire chain so the gate rejects inputs
	// that would fail mid-chain on a fail-over hop. A user-visible
	// "switch model" error is cheaper than a silent upstream 400.
	return b.mgr.ChainSupports(func(key string) modelregistry.SupportsFlags {
		if key == "" {
			return modelregistry.SupportsFlags{}
		}
		// Manifest baseline — provides operational flags (Streaming,
		// PromptCaching, …) and the default capability set.
		var base modelregistry.SupportsFlags
		if m := b.reg.LookupModel(key); m != nil {
			base = m.Supports
		}
		// Endpoint override: only the 7 token-mapped capability fields
		// are replaced; operational flags from the manifest survive.
		// Returns false from LookupEndpointModelType when no model_type
		// is configured → bridge transparently falls back to manifest.
		if tokens, ok := b.mgr.LookupEndpointModelType(key); ok {
			return modelregistry.MergeOverride(base, modelregistry.SupportsFromTokens(tokens))
		}
		return base
	})
}
