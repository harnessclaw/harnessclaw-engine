//go:build !windows

package bash

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func resolveShell() string {
	if s := os.Getenv("CLAUDE_CODE_SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}

func buildShellArgs(cwd string, userCommand string, cwdFilePath string) []string {
	// Run the user command in the current shell (no subshell) so cd takes effect.
	// Capture exit code, then persist the resulting cwd for the next tool call.
	wrapped := fmt.Sprintf(
		`cd %s && %s
__ec=$?; pwd -P > %s; exit $__ec`,
		shellQuote(cwd), userCommand, shellQuote(cwdFilePath),
	)
	return []string{"-c", wrapped}
}

func configureShellCommand(cmd *exec.Cmd) {
	// Create a new process group so cancellation can terminate the full tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
