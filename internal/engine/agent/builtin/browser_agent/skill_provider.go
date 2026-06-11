package browser_agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"harnessclaw-go/internal/engine/agent/builtin/browser_agent/resources"
	"harnessclaw-go/internal/config"
	"harnessclaw-go/internal/skills"
)

// adapterHeader is prepended to every official skill body so the LLM knows
// how to translate CLI instructions into harnessclaw tool calls.
const adapterHeader = `HarnessClaw adapter:
- You cannot call Bash or shell commands.
- Whenever the official skill says to run ` + "`agent-browser ...`" + `, call ` + "`agent_browser_command`" + ` instead.
- Put the CLI subcommand and arguments into ` + "`args[]`" + ` exactly as argv items.
- Do not include the binary name, ` + "`--cdp`" + `, ` + "`--session`" + `, or ` + "`--json`" + ` in ` + "`args[]`" + `; HarnessClaw adds them.
- HarnessClaw binds the latest ` + "`cdp_endpoint`" + ` from ` + "`browser_session_create`" + ` or ` + "`browser_session_state`" + ` to this Browser Agent; do not invent or reuse endpoints from another Browser Agent.
- If this SKILL.md points to a reference you need, call ` + "`browser_skill_reference({\"path\":\"references/<name>.md\"})`" + ` instead of guessing from memory.
- Finish with ` + "`browser_agent_final_result`" + `; do not call ` + "`submit_task_result`" + ` directly.
- For login, CAPTCHA, QR scan, MFA, or site confirmation, call ` + "`browser_ask_human`" + `, then ` + "`browser_session_state`" + `, then continue.
- Do not close the browser after ordinary tasks; Electron hides the window and keeps the persistent profile.`

// SkillProvider loads the official agent-browser skill body at runtime.
type SkillProvider interface {
	Load(ctx context.Context) (*skill.SkillFull, error)
}

// AgentBrowserSkillProvider loads the embedded official skill from the engine.
// Browser-agent is a bundled operation layer, not a PATH dependency.
type AgentBrowserSkillProvider struct {
	cfg        config.BrowserAgentConfig
	sourcePath string
	readBody   func() (string, error)
	logger     *zap.Logger
}

// NewAgentBrowserSkillProvider creates a provider backed by embedded resources.
func NewAgentBrowserSkillProvider(cfg config.BrowserAgentConfig, logger *zap.Logger) *AgentBrowserSkillProvider {
	return &AgentBrowserSkillProvider{
		cfg:        cfg,
		sourcePath: browseragentresources.SkillPath,
		readBody:   browseragentresources.SkillBody,
		logger:     logger,
	}
}

func newAgentBrowserSkillProviderForTest(cfg config.BrowserAgentConfig, sourcePath string, logger *zap.Logger) *AgentBrowserSkillProvider {
	return &AgentBrowserSkillProvider{
		cfg:        cfg,
		sourcePath: sourcePath,
		readBody:   func() (string, error) { return readSkillBodyFromFile(sourcePath) },
		logger:     logger,
	}
}

// Load returns the embedded official skill.
func (p *AgentBrowserSkillProvider) Load(ctx context.Context) (*skill.SkillFull, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sourcePath := strings.TrimSpace(p.sourcePath)
	readBody := p.readBody
	if readBody == nil {
		readBody = browseragentresources.SkillBody
	}
	body, err := readBody()
	if err != nil {
		return nil, err
	}
	if body == "" {
		return nil, fmt.Errorf("agent-browser embedded skill is empty at %s", sourcePath)
	}

	maxBytes := p.cfg.SkillMaxBytes
	if maxBytes > 0 && len(body) > maxBytes {
		return nil, fmt.Errorf("agent-browser skill body too large: %d bytes (max %d)", len(body), maxBytes)
	}

	fullBody := adapterHeader + "\n\n" + body

	return &skill.SkillFull{
		SkillCard: skill.SkillCard{
			Name:    "agent-browser/core",
			Version: "embedded",
			Path:    sourcePath,
		},
		Body: fullBody,
	}, nil
}

func readSkillBodyFromFile(sourcePath string) (string, error) {
	main, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("agent-browser skill read failed at %s: %w", sourcePath, err)
	}
	return strings.TrimSpace(string(main)), nil
}
