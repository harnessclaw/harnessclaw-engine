// Package browser_agent runs the browser-agent leaf sub-agent.
package browser_agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/engine/agent/common"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/loop"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/provider"
	"harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	browsertools "harnessclaw-go/internal/tool/browser"
	"harnessclaw-go/internal/tool/submittool"
	"harnessclaw-go/pkg/types"
)

const submitRetries = 3

type Deps struct {
	Provider           provider.Provider
	Registry           *tool.Registry
	SessionMgr         *session.Manager
	Compactor          compact.Compactor
	Retryer            *retry.Retryer
	PromptBuilder      *prompt.Builder
	Logger             *zap.Logger
	MaxTokens          int
	ContextWindow      int
	BrowserAgentConfig config.BrowserAgentConfig
	SkillProvider      SkillProvider
}

type Module struct {
	deps Deps
}

func New(deps Deps) *Module {
	if deps.SkillProvider == nil && deps.BrowserAgentConfig.Enabled {
		deps.SkillProvider = NewAgentBrowserSkillProvider(deps.BrowserAgentConfig, deps.Logger)
	}
	return &Module{deps: deps}
}

func (m *Module) SubagentType() string { return agent.BrowserAgentName }

func (m *Module) Run(ctx context.Context, cfg *agent.SpawnConfig) (*agent.SpawnResult, error) {
	startTime := time.Now()
	def := agent.BrowserAgentDefinition()

	if len(cfg.Inputs) > 0 {
		if fails := submittool.ValidateAgainstSchema(def.InputSchema, cfg.Inputs); len(fails) > 0 {
			return nil, fmt.Errorf("input schema validation failed for %q: %s", cfg.SubagentType, strings.Join(fails, "; "))
		}
	}

	sess, err := common.BuildSubSession(m.deps.SessionMgr, cfg.ParentSessionID)
	if err != nil {
		return nil, err
	}

	taskID := cfg.TaskID
	if strings.TrimSpace(taskID) == "" {
		taskID = "browser_" + sess.ID
	}
	browserBinding := browsertools.NewTaskBinding(taskID)
	ctx = browsertools.WithTaskBinding(ctx, browserBinding)
	defer m.cleanupHelperSession(ctx, taskID)

	pool := common.BuildToolPool(m.deps.Registry, def.MaybeAugmentForSubAgent(), cfg.AgentType, true)

	browserSkillBlock := ""
	if m.deps.SkillProvider != nil {
		full, err := m.deps.SkillProvider.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("browser agent official skill load failed: %w", err)
		}
		browserSkillBlock = prompt.BuildLoadedSkillsBlock([]*skill.SkillFull{full})
	}

	sysPrompt := joinNonEmpty([]string{
		def.SystemPrompt,
		browserSkillBlock,
		agent.RenderSubAgentContract(def),
		common.BuildSubAgentPrompt(common.PromptArgs{
			Ctx:               ctx,
			Session:           sess,
			Profile:           prompt.WorkerProfile,
			Builder:           m.deps.PromptBuilder,
			WorkerDisplayName: def.DisplayName,
			SubagentType:      def.Name,
			ContextWindow:     m.deps.ContextWindow,
			Registry:          m.deps.Registry,
			AvailableTools:    pool.All(),
		}),
	}, "\n\n")

	common.EmitSubagentStart(cfg.ParentOut, common.StartEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentDesc:       cfg.Description,
		AgentTask:       cfg.Prompt,
		AgentType:       string(cfg.AgentType),
		SubagentType:    agent.BrowserAgentName,
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
		ParentStepID:    cfg.ParentStepID,
	})

	ctx = common.WithSubAgentStats(ctx, sess.ID, sess.ID, cfg.ParentSessionID, cfg.RootSessionID)

	sess.AddMessage(types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText,
			Text: browserTaskPrompt(taskID, cfg.Prompt),
		}},
	})

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 30
	}

	clientAwaitSession := m.clientAwaitSession(cfg)
	finalEnforcer := common.SubmitResultEnforcerForTool(browsertools.FinalResultToolName, nil, def.OutputSchema, submitRetries)
	permChecker := common.BuildInheritedChecker(
		common.SessionApprovedTools(m.deps.SessionMgr, cfg.ParentSessionID),
	)
	loopRes, err := loop.Run(ctx, &loop.Config{
		Session:            sess,
		SystemPrompt:       sysPrompt,
		Tools:              pool,
		Provider:           m.deps.Provider,
		Compactor:          m.deps.Compactor,
		Retryer:            m.deps.Retryer,
		Logger:             m.deps.Logger,
		ClientAwaitSession: clientAwaitSession,
		MaxTurns:           maxTurns,
		MaxTokens:          m.deps.MaxTokens,
		ContextWindow:      m.deps.ContextWindow,
		Out:                cfg.ParentOut,
		AgentID:            sess.ID,
		PermChecker:        permChecker,
		ApprovalFn:         nil,
		TaskContract:       tool.TaskContract{TaskID: taskID, TaskStartedAt: cfg.TaskStartedAt, OutputSchema: def.OutputSchema},
		ArtifactProducer:   tool.ArtifactProducer{AgentID: sess.ID, AgentRunID: sess.ID, TaskID: taskID, SessionID: sess.ID},
		OnTurnComplete: func(turn int, msg types.Message, results []types.ToolResult) loop.Decision {
			browsertools.UpdateTaskBindingFromResults(msg, results, browserBinding)
			return finalEnforcer(turn, msg, results)
		},
	})
	if err != nil {
		return nil, err
	}

	output := acceptedSubmitContent(loopRes.LastToolResults)
	if output == "" && loopRes.LastMessage != nil {
		for _, b := range loopRes.LastMessage.Content {
			if b.Type == types.ContentTypeText {
				output += b.Text
			}
		}
	}

	terminal := loopRes.Terminal
	usage := loopRes.Usage
	common.EmitSubagentEnd(cfg.ParentOut, common.EndEvent{
		AgentID:         sess.ID,
		AgentName:       cfg.Name,
		AgentStatus:     statusFromTerminal(terminal),
		SubagentType:    agent.BrowserAgentName,
		DurationMs:      time.Since(startTime).Milliseconds(),
		Usage:           &usage,
		Terminal:        &terminal,
		ParentAgentID:   cfg.ParentAgentID,
		ParentSessionID: cfg.ParentSessionID,
	})

	return common.BuildSpawnResult(sess.ID, sess.ID, output, terminal, usage, loopRes.NumTurns), nil
}

func (m *Module) clientAwaitSession(cfg *agent.SpawnConfig) *session.Session {
	if m.deps.SessionMgr == nil || cfg == nil {
		return nil
	}
	for _, id := range []string{cfg.RootSessionID, cfg.ParentSessionID} {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if sess := m.deps.SessionMgr.Get(id); sess != nil {
			return sess
		}
	}
	return nil
}

func browserTaskPrompt(taskID, body string) string {
	return joinNonEmpty([]string{
		"<spawn-info>\n" +
			"task_id: " + taskID + "\n" +
			"final_result: 调用 browser_agent_final_result({\"content\":\"...\", \"source\":\"browser|partial\"})；框架会自动绑定 task_id。\n" +
			"result 必须符合 output_schema；本 Agent 不需要写文件产物，也不要直接调用 submit_task_result。\n" +
			"</spawn-info>",
		"<task>\n" + body + "\n</task>",
	}, "\n\n")
}

func (m *Module) cleanupHelperSession(ctx context.Context, taskID string) {
	if !m.deps.BrowserAgentConfig.Enabled || strings.TrimSpace(taskID) == "" {
		return
	}
	if _, err := browsertools.CleanupHelperSession(context.WithoutCancel(ctx), m.deps.BrowserAgentConfig, nil, taskID); err != nil && m.deps.Logger != nil {
		m.deps.Logger.Debug("browser agent helper cleanup failed", zap.String("task_id", taskID), zap.Error(err))
	}
}

func acceptedSubmitContent(results []types.ToolResult) string {
	for _, result := range results {
		if result.Metadata == nil {
			continue
		}
		hint, _ := result.Metadata["render_hint"].(string)
		accepted, _ := result.Metadata[submittool.MetadataKeyAccepted].(bool)
		if hint == submittool.MetadataRenderHint && accepted {
			return result.Content
		}
	}
	return ""
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, sep)
}

func statusFromTerminal(t types.Terminal) string {
	switch t.Reason {
	case types.TerminalCompleted:
		return "completed"
	case types.TerminalMaxTurns:
		return "max_turns"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		return "aborted"
	default:
		return "failed"
	}
}
