//go:build windows

package bash

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func resolveShell() string {
	if s := os.Getenv("CLAUDE_CODE_SHELL"); s != "" {
		return s
	}
	if _, err := exec.LookPath("pwsh.exe"); err == nil {
		return "pwsh.exe"
	}
	return "powershell.exe"
}

func buildShellArgs(cwd string, userCommand string, cwdFilePath string) []string {
	script := fmt.Sprintf(
		`$ErrorActionPreference = 'Continue'
Set-Location -LiteralPath %s
& {
%s
}
$__hc_exit = if ($null -ne $LASTEXITCODE) { [int]$LASTEXITCODE } elseif ($?) { 0 } else { 1 }
(Get-Location).Path | Set-Content -LiteralPath %s -NoNewline
exit $__hc_exit`,
		powershellQuote(cwd), userCommand, powershellQuote(cwdFilePath),
	)
	return []string{"-NoLogo", "-NoProfile", "-Command", script}
}

func configureShellCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T", "/F")
		kill.Env = os.Environ()
		output, err := kill.CombinedOutput()
		if err != nil {
			return fmt.Errorf("taskkill failed: %w (%s)", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
}

func powershellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
