package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func NewCommandRunner(cfg config.BrowserAgentConfig) *CommandRunner {
	return &CommandRunner{binaryPath: browserBinary(cfg)}
}

func (r *CommandRunner) Run(ctx context.Context, args []string) ([]byte, error) {
	return r.command(ctx, args).CombinedOutput()
}

func (r *CommandRunner) command(ctx context.Context, args []string) *exec.Cmd {
	if nodePath, scriptPath, ok := nodeScriptCommand(r.binaryPath); ok {
		cmdArgs := append([]string{scriptPath}, args...)
		return exec.CommandContext(ctx, nodePath, cmdArgs...)
	}
	return exec.CommandContext(ctx, r.binaryPath, args...)
}

func browserBinary(cfg config.BrowserAgentConfig) string {
	if cfg.BinaryPath != "" {
		return cfg.BinaryPath
	}
	return "agent-browser"
}

func cliTimeout(cfg config.BrowserAgentConfig) time.Duration {
	if cfg.CLITimeout > 0 {
		return cfg.CLITimeout
	}
	return 25 * time.Second
}

func nodeScriptCommand(binaryPath string) (nodePath string, scriptPath string, ok bool) {
	resolved := binaryPath
	if !filepath.IsAbs(resolved) {
		path, err := exec.LookPath(binaryPath)
		if err != nil {
			return "", "", false
		}
		resolved = path
	}
	line, err := readShebang(resolved)
	if err != nil || !strings.Contains(line, "node") {
		return "", "", false
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", "", false
	}
	return node, resolved, true
}

func readShebang(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	line := string(buf[:n])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if !strings.HasPrefix(line, "#!") {
		return "", nil
	}
	return line, nil
}
