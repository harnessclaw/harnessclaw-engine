//go:build windows

package bash

import (
	"os"
	"os/exec"
)

// setProcAttr is a no-op on Windows; the default cancel behaviour (Process.Kill) is used.
func setProcAttr(cmd *exec.Cmd) {}

// resolveShell finds the preferred shell binary on Windows.
func resolveShell() string {
	if s := os.Getenv("CLAUDE_CODE_SHELL"); s != "" {
		return s
	}
	if s := os.Getenv("COMSPEC"); s != "" {
		return s
	}
	return "cmd.exe"
}
