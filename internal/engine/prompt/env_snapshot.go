package prompt

import (
	"os"
	"runtime"
	"time"
)

// BuildEnvSnapshot constructs a point-in-time EnvSnapshot for the LLM's
// situational awareness（platform / OS / CWD / shell / date）。
//
// cwd 为空时回落到 "~/.harnessclaw/workspace"。同一逻辑此前散落在
// emma.Engine.getEnvSnapshot 等多处 —— 抽出来让 emma 主路径和 sub-agent
// dispatch 路径（loop/runtime/llm.go）共享同一构造逻辑。
func BuildEnvSnapshot(cwd string) EnvSnapshot {
	snap := EnvSnapshot{
		OS:       runtime.GOOS,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Date:     time.Now().Format("2006-01-02"),
	}
	if cwd != "" {
		snap.CWD = cwd
	} else {
		snap.CWD = "~/.harnessclaw/workspace"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		snap.Shell = shell
	} else if comspec := os.Getenv("COMSPEC"); comspec != "" {
		snap.Shell = comspec
	}
	return snap
}
