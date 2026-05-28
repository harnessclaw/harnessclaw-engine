// internal/engine/spawn/fake_deps_test.go
package spawn

import (
	"context"
	"time"

	"go.uber.org/zap"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/command"
	"harnessclaw-go/internal/engine/compact"
	"harnessclaw-go/internal/engine/prompt"
	"harnessclaw-go/internal/engine/session"
	"harnessclaw-go/internal/engine/sessionstats"
	enginesched "harnessclaw-go/internal/engine/scheduler"
	"harnessclaw-go/internal/event"
	"harnessclaw-go/internal/permission"
	"harnessclaw-go/internal/provider"
	provretry "harnessclaw-go/internal/provider/retry"
	"harnessclaw-go/internal/skill"
	"harnessclaw-go/internal/tool"
	"harnessclaw-go/pkg/types"
)

// fakeDeps implements spawn.Deps for wiring smoke tests. It does NOT
// exercise real behavior — every method returns a placeholder. The
// purpose is to catch regressions where a new Deps method is added but
// the QueryEngine implementation forgets to wire it.
type fakeDeps struct {
	logger   *zap.Logger
	eventBus *event.Bus
}

func newFakeDeps() *fakeDeps {
	return &fakeDeps{
		logger:   zap.NewNop(),
		eventBus: event.NewBus(),
	}
}

// --- state accessors ---

func (f *fakeDeps) Logger() *zap.Logger {
	return f.logger
}

func (f *fakeDeps) SpawnerConfig() SpawnConfig {
	return SpawnConfig{
		MaxTurns:             10,
		AutoCompactThreshold: 0.8,
		ToolTimeout:          30 * time.Second,
		MaxTokens:            4000,
		ContextWindow:        200000,
		SystemPrompt:         "",
		ClientTools:          false,
		MainAgentDisplayName: "test",
		MaxPlanReplans:       3,
		MaxStepAttempts:      3,
		LLMMaxRetries:        5,
		LLMAPITimeout:        60 * time.Second,
		LLMFirstByteTimeout:  30 * time.Second,
	}
}

func (f *fakeDeps) Provider() provider.Provider {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) Registry() *tool.Registry {
	return tool.NewRegistry()
}

func (f *fakeDeps) CmdRegistry() *command.Registry {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) Compactor() compact.Compactor {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) PermChecker() permission.Checker {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) EventBus() *event.Bus {
	return f.eventBus
}

func (f *fakeDeps) SessionMgr() *session.Manager {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) StatsRegistry() *sessionstats.Registry {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) DefRegistry() *agent.AgentDefinitionRegistry {
	return agent.NewAgentDefinitionRegistry()
}

func (f *fakeDeps) SkillReader() *skill.Reader {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) PromptBuilder() *prompt.Builder {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) SchedulerCoord() *enginesched.Coordinator {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) Retryer() *provretry.Retryer {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) SelfSpawner() agent.AgentSpawner {
	return nil // stub — not exercised by smoke test
}

// --- helper wrappers ---

func (f *fakeDeps) LLMTimeouts() LLMTimeouts {
	return LLMTimeouts{
		API:       60 * time.Second,
		FirstByte: 30 * time.Second,
	}
}

func (f *fakeDeps) CallLLM(
	ctx context.Context,
	req *provider.ChatRequest,
	logger *zap.Logger,
	agentID string,
	out, planningOut chan<- types.EngineEvent,
) LLMCallResult {
	return LLMCallResult{}
}

func (f *fakeDeps) NewToolExecutor(
	pool *tool.ToolPool,
	perm permission.Checker,
	logger *zap.Logger,
	timeout time.Duration,
	approvalFn ToolApprovalFunc,
) ToolExecutor {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) DispatchToolBatch(
	ctx context.Context,
	sess *session.Session,
	executor ToolExecutor,
	pool *tool.ToolPool,
	toolCalls []types.ToolCall,
	out chan<- types.EngineEvent,
) []types.ToolResult {
	return nil // stub — not exercised by smoke test
}

func (f *fakeDeps) BuildAssistantMessage(
	text string,
	toolCalls []types.ToolCall,
	usage *types.Usage,
	reasoning string,
) types.Message {
	return types.Message{}
}

func (f *fakeDeps) EffectiveContextWindow(configured int) int {
	return 200000
}

func (f *fakeDeps) ContextWindow() int {
	return 200000
}

func (f *fakeDeps) GetSkillListingFiltered(allowedSkills map[string]bool) string {
	return ""
}

func (f *fakeDeps) GetEnvSnapshot(sessionRoot string) prompt.EnvSnapshot {
	return prompt.EnvSnapshot{}
}

func (f *fakeDeps) GetSessionApprovedTools(sessionID string) []string {
	return nil
}

func (f *fakeDeps) BuildLoadedSkillsBlock(fulls []*skill.SkillFull) string {
	return ""
}

func (f *fakeDeps) AgentRegistry() *agent.AgentRegistry {
	return agent.NewAgentRegistry()
}

func (f *fakeDeps) MessageBroker() *agent.MessageBroker {
	return nil // stub — not exercised by smoke test
}
