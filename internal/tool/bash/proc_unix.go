//go:build !windows

package bash

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcAttr creates a new process group and sets up process-group kill on cancel.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// resolveShell finds the preferred shell binary.
func resolveShell() string {
	if s := os.Getenv("CLAUDE_CODE_SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}
