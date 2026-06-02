package common

import (
	"fmt"

	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/workspace"
)

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
func WorkspacePrelude(cfg *agent.SpawnConfig, rootDir string) string {
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
func SeedPrompt(cfg *agent.SpawnConfig, rootDir string) string {
	prelude := WorkspacePrelude(cfg, rootDir)
	if prelude == "" {
		return cfg.Prompt
	}
	return prelude + cfg.Prompt
}
