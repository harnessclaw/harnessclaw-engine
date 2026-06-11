package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"harnessclaw-go/internal/config"
)

// Runner executes agent-browser CLI commands. Tests replace it with a fake so
// tool behavior is covered without launching a real browser.
type Runner interface {
	Run(ctx context.Context, args []string) ([]byte, error)
}

type CommandRunner struct {
	binaryPath string
}

const agentBrowserBinaryPathEnv = "CLAUDE_TOOLS_BROWSER_AGENT_BINARY_PATH"

func NewCommandRunner(_ config.BrowserAgentConfig) *CommandRunner {
	return &CommandRunner{binaryPath: browserBinary()}
}

func (r *CommandRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	return r.command(ctx, args).CombinedOutput()
}

func (r *CommandRunner) command(ctx context.Context, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, r.binaryPath, args...)
}

func browserBinary() string {
	if path := strings.TrimSpace(os.Getenv(agentBrowserBinaryPathEnv)); path != "" {
		return path
	}
	return packagedAgentBrowserBinaryPath()
}

func cliTimeout(cfg config.BrowserAgentConfig) time.Duration {
	if cfg.CLITimeout > 0 {
		return cfg.CLITimeout
	}
	return 25 * time.Second
}

func packagedAgentBrowserBinaryPath() string {
	name := agentBrowserBinaryName()
	if name == "" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(string(os.PathSeparator), "missing-harnessclaw-runtime", name)
	}
	return filepath.Join(filepath.Dir(exe), name)
}

func agentBrowserBinaryName() string {
	osKey := runtime.GOOS
	archKey := runtime.GOARCH
	switch runtime.GOARCH {
	case "amd64":
		archKey = "x64"
	case "arm64":
		archKey = "arm64"
	default:
		return ""
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		return "agent-browser-" + osKey + "-" + archKey
	case "windows":
		return "agent-browser-win32-" + archKey + ".exe"
	default:
		return ""
	}
}
