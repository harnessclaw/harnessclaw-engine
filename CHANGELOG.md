# Changelog

All notable changes to this project will be documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/), and versions are published to GitHub Releases.

## [0.0.6] - 2026-04-27

### Added
- Agent definition persistence: SQLite-backed `AgentService` and console HTTP API (`/console/v1/agents`) for create/list/get/update/delete/import operations; built-in definitions are synced on startup and YAML files are imported on demand
- Console management API server with configurable host/port (`console.enabled/host/port` config keys, defaults to `0.0.0.0:8090`)
- Per-agent skill whitelist enforcement: `AgentDefinition.Skills` and `SpawnConfig.AllowedSkills` are now respected by `SkillTool`, which rejects invocations of skills not on the list
- Per-agent tool whitelist enforcement: `AgentDefinition.AllowedTools` filters the sub-agent tool pool via the new `ToolPool.FilterByNames` method
- `Personality` field is injected into the auto-generated worker identity prompt
- WebSocket `deliverable.ready` event: sub-agent file outputs (FileWrite results) surface to the client with `file_path`, `language`, and `byte_size` for direct rendering or download
- `<summary>` tag protocol for sub-agent outputs: Worker / Explore / Plan profiles require sub-agents to wrap their core conclusion in `<summary>...</summary>`; the engine extracts it into `SpawnResult.Summary` and returns only summary + deliverables to the parent agent
- `TaskRegistry`: full sub-agent results are stored in-engine by agent ID for context passing and debugging while the parent only sees summaries
- `SpawnResult` now carries structured `Summary`, `Status`, and `Attempts` fields

### Changed
- Emma system prompt restructured into a three-layer architecture: persona/team/judgment/delivery only; dispatch protocol, retry rules, and multi-agent coordination paragraphs moved to application code
- Sub-agent system prompts no longer hard-code agent names in role overrides; dynamic `SystemPromptOverride` from agent definition takes precedence over static profile `SectionOverrides` for the role section
- Section headings dropped numeric prefixes ("一、二、三") so reordering does not break the prompt
- All built-in section content translated to Chinese (env, tools, memory, skills, task, currentdate, artifacts)
- `FileWrite` no longer auto-creates parent directories; callers must ensure the directory exists or the tool returns an explicit error pointing at the missing path; the schema description now hints at a default working directory
- Agent definitions are no longer auto-scanned from `.harnessclaw/agents/` on startup; use the import endpoint instead

### Removed
- Coordinator system prompt and `CoordinatorProfile`: orchestration is now an L2 application-code concern, planned to land as a code-driven Orchestrate tool in a follow-up
- Static `output` / `rules` prompt sections: their delivery rules are folded into `principles`

## [0.0.5] - 2026-04-22

### Added
- Universal artifact store: session-scoped content store for large tool results with automatic threshold-based replacement, frozen replacement decisions for prompt cache stability, and pre-LLM compaction
- ArtifactGet tool: LLM retrieves full artifact content by ID without regenerating
- Write tool `artifact_ref` parameter: write artifact content to files by reference, saving output tokens
- Artifact-aware compaction: replaces artifact-backed tool results with compact references before LLM summarization
- SQLite artifact persistence: `artifacts` table for persisting large tool results across server restarts
- Multi-agent orchestration system: sub-agent spawning (sync/async/fork), loopConfig-parameterized query loop, coordinator mode with team management
- Agent tool with `SpawnSync`/`SpawnAsync`, `InheritedChecker` permission inheritance, and `LongRunningTool` interface for timeout bypass
- Task system: `TaskCreate`/`TaskGet`/`TaskList`/`TaskUpdate` tools with in-memory and SQLite-backed stores
- Team management: `TeamCreate`/`TeamDelete` tools, `MessageBroker` with mailbox-based inter-agent messaging, `SendMessage` tool
- @-mention routing: `MentionParser` extracts `@agent_name` from user messages, routes to registered agent definitions loaded from YAML
- Coordinator mode: system prompt rewrite to dispatcher role with 4-phase workflow (research → synthesis → implementation → verification)
- WebSocket sub-agent and multi-agent event protocol: `subagent.start/end`, `agent.routed/spawned/idle/completed/failed`, `task.created/updated`, `agent.message`, `team.created/member_join/member_left/deleted`
- Render hint metadata on tool results: `render_hint`, `language`, `file_path` fields promoted to top-level in `tool.end` WebSocket messages
- Language detection utility mapping file extensions to language identifiers for render hints
- Web search tools: Tavily search and iFly/Xunfei search integrations
- LLM retry with exponential backoff for transient provider failures
- Mock LLM provider and stream builder for unit testing
- Current date section in system prompt
- Artifact guidance section in system prompt teaching LLM about artifact usage patterns

### Changed
- Storage architecture: removed memory/sqlite switch; SQLite is now always the persistence backend, `Manager.active` map serves as in-memory cache
- Bifrost adapter error messages now include HTTP status code, error type/code, and underlying error details for easier troubleshooting
- Bifrost stream idle timeout increased to 300s and request timeout to 600s for sub-agent workloads
- System prompt role and output style sections rewritten with improved anti-AI-speak guidance

### Fixed
- Session persistence across server restarts: eliminated config-level memory/sqlite choice that prevented SQLite from being used
- Bifrost error messages no longer hide HTTP-level error details behind generic constant strings
- Missing `SourceSkillDir` constant in command source enumeration

## [0.0.4] - 2026-04-18

### Added
- Nested skill directory discovery: supports `skills/<repo-name>/<skill-name>/SKILL.md` multi-level layouts via recursive `filepath.WalkDir`
- Commit and changelog rules documentation (`docs/release-rules.md`) covering conventional commit format, Co-Authored-By prohibition, and LLM-based changelog generation policy

### Changed
- GitHub Release body now extracted from `CHANGELOG.md` instead of auto-generated commit list, ensuring release notes match the maintained changelog

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
