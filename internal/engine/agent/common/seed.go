package common

import (
	"fmt"
	"os"

	"harnessclaw-go/internal/workspace"
)

// 注：旧 EnsureTaskDir(cfg, rootDir) 已删 —— 之前在 dispatch sub-agent 启动
// 前预 mkdir task_dir，导致只读 sub-agent（如纯调 web_search 后回报）也
// 会在磁盘留空目录。改为 lazy 创建：write / edit / meta_write 工具在
// 写入前自己 MkdirAll(filepath.Dir(path))，需要落盘的 sub-agent 自然
// 触发；不落盘的不留垃圾目录。
//
// 如果未来需要重新启用预创建（比如某些工具不便延迟），用
// workspace.EnsureTaskDir(rootDir, sessionID, taskID) 这个 helper。

// WorkspacePrelude returns a short, natural-language paragraph telling
// a sub-agent where its work directory lives. Prepended to the user
// prompt by tier modules so the LLM doesn't have to guess (or call
// bash pwd) just to find where to write produce.
//
// Returns "" when the rootDir or session id is missing — the caller
// should then just send cfg.Prompt verbatim (no prelude is better than
// a half-broken one). Empty is also returned when no TaskID is set
// (true for non-plan dispatches that don't carve a task subdir).
//
// The text is intentionally short and human-readable — no XML tags, no
// machine-parseable schema. meta.json (written via meta_write) remains
// the durable source of truth for task identity / output paths; this
// is a navigation hint only.
func WorkspacePrelude(cfg *SpawnConfig, rootDir string) string {
	if cfg == nil || rootDir == "" || cfg.RootSessionID == "" {
		return ""
	}
	sessionRoot := workspace.SessionRoot(rootDir, cfg.RootSessionID)
	if cfg.TaskID == "" {
		// No per-task subdir — fall back to a session-scoped hint.
		return fmt.Sprintf(
			"工作上下文（framework 注入，非 LLM 输入）：\n"+
				"- session 根目录：%s\n"+
				"- 产物落在 session 根目录内，路径用绝对路径\n\n",
			sessionRoot,
		)
	}
	taskDir := workspace.TaskDir(rootDir, cfg.RootSessionID, cfg.TaskID)
	return fmt.Sprintf(
		"工作上下文（framework 注入，非 LLM 输入）：\n"+
			"- task_id：%s\n"+
			"- 产物目录（task_dir）：%s\n"+
			"- 所有 write/edit 的产物文件必须落在上述 task_dir 内（绝对路径），否则 write_scope 拒\n"+
			"- meta.json 是事实标准；task_id / meta_path 由 framework 通过 ctx 注入 meta_write / submit_task_result，调用时不要传\n\n",
		cfg.TaskID, taskDir,
	)
}

// SeedPrompt returns the full text to use as the first user message:
// WorkspacePrelude (if available) followed by cfg.Prompt.
func SeedPrompt(cfg *SpawnConfig, rootDir string) string {
	prelude := WorkspacePrelude(cfg, rootDir)
	if prelude == "" {
		return cfg.Prompt
	}
	return prelude + cfg.Prompt
}

// ScanResidualFiles lists every regular file currently living under the
// spawn's task_dir. Non-recursive: we only surface the top level (where
// task output is supposed to land) — nested scratch dirs would balloon
// the failure summary the parent reads. Returns nil (not error) for
// missing dir or unreadable entries; this is best-effort observability,
// not a contract.
//
// Tier modules call this right before returning their SpawnResult, so
// the file list reaches the parent via BuildFailureContent and the
// parent LLM has a chance to resume rather than restart. See
// SpawnResult.ResidualFiles docstring for the recovery rationale.
func ScanResidualFiles(cfg *SpawnConfig, rootDir string) []ResidualFile {
	if cfg == nil || rootDir == "" || cfg.RootSessionID == "" || cfg.TaskID == "" {
		return nil
	}
	dir := workspace.TaskDir(rootDir, cfg.RootSessionID, cfg.TaskID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]ResidualFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, ResidualFile{
			Path:      dir + string(os.PathSeparator) + e.Name(),
			SizeBytes: info.Size(),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
