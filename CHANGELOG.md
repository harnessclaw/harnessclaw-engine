# Changelog

All notable changes to this project will be documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/), and versions are published to GitHub Releases.

## [0.0.3] - 2026-04-18

### Added
- Skill listing migrated from per-turn `<system-reminder>` user message injection (Layer 3) to a dedicated SkillsSection in the prompt builder (Layer 2), enabling token budget allocation and API-level prompt caching
- Structured prompt builder with modular sections (role, principles, output, tools, env, memory, skills, task), token budget allocation, and section-level priority/cacheability
- Per-session whole-output prompt caching in query loop — avoids rebuilding system prompt when inputs haven't changed
- Prompt observability: PROMPT DUMP and LLM REQUEST DUMP logging for debugging system prompt assembly
- Skill execution metrics logging: skill name, args, duration, prompt length, context mode, and model override
- Structured skill load result (`LoadResult`/`LoadError`) with per-error detail reporting at startup
- Default skills directory (`~/.harnessclaw/workspace/skills/`) when config `skills.dirs` is null or empty
- Cross-platform path expansion for skill directories: supports `~/` (Unix) and `~\` (Windows) prefix, normalizes separators via `filepath.FromSlash`
- Dynamic environment detection in system prompt (`runtime.GOOS`, `os.Getenv("SHELL")`) replacing hardcoded placeholders
- Task state rendering section with goal, plan progress markers (✓/→), completed steps, and blockers
- Harness agent execution principles section covering planning, action rules, failure recovery, and stop conditions

### Changed
- System prompt now assembled by prompt builder with 7 sections instead of a single static string
- Skill listing description hard cap increased from 250 to 500 characters
- Removed `IsGit`, `GitBranch`, `GitStatus` from `EnvSnapshot` — environment section now shows only OS, platform, shell, and working directory

### Removed
- Section-level cache from prompt builder (replaced by whole-output cache at query loop level to avoid cross-session pollution)

## [0.0.2] - 2026-04-16

### Added
- WebSocket error frame (`error` type) now emitted to client when LLM/model requests fail, providing structured error details (type, code, message)
- Error detail embedded in `message.delta` frame (`delta.error` field) so clients can extract error info from the message lifecycle
- Terminal message field in `task.end` frame to surface human-readable failure reasons
- Invalid JSON parse error feedback to WebSocket clients — previously silent, now returns an `error` frame

### Changed
- Replaced direct Anthropic SDK with Bifrost SDK (`github.com/maximhq/bifrost/core`) as the unified multi-provider LLM backend, enabling support for OpenAI, Anthropic, Bedrock, Vertex, and other providers through a single adapter
- Query loop now emits complete message lifecycle (`error` → `message.delta` → `message.stop`) on both `Chat()` connection failures and mid-stream errors

### Fixed
- WebSocket clients no longer receive silent failures on invalid JSON messages; an `error` frame with parse details is returned
- Model errors (authentication, rate-limit, provider failures) are now fully propagated through the WebSocket wire protocol instead of only signaling `stop_reason: "error"` without details

## [0.0.1] - 2026-04-13

### Added
- Initial release of harnessclaw-engine (Go rebuild)
- WebSocket channel with streaming wire protocol (v1.5) supporting text, tool use, thinking, and multi-type content blocks
- Tools system: Bash, FileRead, FileEdit, FileWrite, Grep, Glob, WebFetch
- Skills framework with directory/flat-file loading and model/user invocation support
- Permission pipeline with configurable modes (default, plan, bypass, acceptEdits, dontAsk)
- Session management with in-memory storage and idle cleanup
- Query engine with auto-compaction, multi-turn conversation, and tool execution loop
- Event bus for session lifecycle and query tracking
- Cross-platform release workflow (CI)
