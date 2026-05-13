# Changelog

All notable changes to this project will be documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/), and versions are published to GitHub Releases.

## [0.0.12] - 2026-05-14

### Added
- Session metrics dashboard: per-session LLM / sub-agent / tool counters tracked by `sessionstats.Tracker`, persisted as `metrics_json` column on session rows, snapshotted via debounced worker, and served at `GET /api/v1/sessions/{id}/metrics` on the Console port; survives service restart via `Tracker.RestoreFrom`
- Provider-stats decorator: wraps any `provider.Provider` to record per-model token usage (input / output / reasoning / cache hits) into `sessionstats.Registry` from a single ctx-key plumbing point, no per-call instrumentation
- `thinking_tokens` field on `Usage`: bifrost adapter now surfaces upstream reasoning-token counts so the dashboard can break out chain-of-thought cost separately from regular completion tokens
- Model registry catalog (`internal/provider/registry`): YAML manifest declaring `ProviderSpec` (auth / endpoints / quirks) and `ModelSpec` (modalities / supports / limits / defaults) for 8 vendors and 16 models (OpenAI, Anthropic, Google, DeepSeek, Zhipu, Moonshot, MiniMax, iFlyTek); embedded default manifest loaded at startup, queryable by manifest key or by provider+model_id
- Public models endpoint: `GET /api/v1/models` and `GET /api/v1/models/{provider}/{model_id}` on the Console server, JSON-tagged snake_case payload for the front-end capability gate; documented in `docs/api/models-registry-api.md`
- Provider quirks routing in bifrost adapter: `ThinkingParamStyle` selects the wire format per provider (`deepseek_type` → `extra_params.thinking.type`, `openai_effort` → `reasoning.effort`, `anthropic_budget` → `reasoning.max_tokens`, `openrouter` → `reasoning.enabled`); `ExtraParamsPassthroughRequired` and `ToolCallsRequireReasoningField` now gate behavior per-quirk instead of being applied unconditionally
- `enable_thinking` provider config option: per-provider override that drives the new quirk routing without changing the call site
- iFlyTek Spark X2 Flash support: `xunfei/spark-x` (256K context, text-only, function_calling + reasoning + web_search) wired through the registry and bifrost adapter
- Structured tool error classification: `ToolErrorType` (`tool_timeout` / `rate_limit` / `overloaded` / `contract_fail` / `dependency_fail` / `permission_denied` / `invalid_input` / `user_aborted` / `model_error` / `internal`) set by the executor and every built-in tool at the failure site; WebSocket translator emits it as `card.close.error.type` instead of defaulting all failures to `internal`

### Changed
- Session-metrics endpoint moved from the WebSocket channel mux to the Console HTTP server (port 8090) so the front-end has one management origin to consult
- Bifrost adapter `Config` accepts a `*ProviderQuirks` mirror struct; quirks come from the registry at startup and gate behavior that was previously hard-coded (e.g. `ExtraParams` passthrough was always-on; now opt-in only for providers that declare it)
- Claude model `family` field regrouped to `claude4.5` / `claude4.6` / `claude4.7` (was `claude-haiku` / `claude-sonnet` / `claude-opus`) so the UI groups by generation rather than tier

### Fixed
- DeepSeek thinking mode now actually disables reasoning: adapter sends `thinking: {"type": "disabled"}` via ExtraParams per the official docs, and opts into bifrost's `BifrostContextKeyPassthroughExtraParams` so the field reaches the wire (the SDK was silently dropping `Params.ExtraParams` during marshalling, so the legacy `enable_thinking: false` field was both ignored by the server and never sent)
- `reasoning_content` is now preserved on assistant messages across DeepSeek thinking-mode turns when thinking is enabled, and emitted as an empty-string placeholder on tool_calls assistant messages when the provider quirk requires the field present — fixes 400 "reasoning_content must be passed back to the API"
- Reasoning replay across turns is suppressed when `enable_thinking` is disabled, instead of inflating input tokens with every prior turn's chain-of-thought
- Bifrost adapter waits for the synthetic usage chunk after `finish_reason` instead of emitting `MessageEnd` immediately, so token totals are no longer lost when bifrost's OpenAI provider holds usage back to a trailing chunk
- Per-model stats now key off the provider-reported model name (echoed back in `Usage`) instead of the adapter's configured default, so usage attribution stays correct when fallbacks or model overrides kick in
- New session rows are persisted eagerly in `Manager.GetOrCreate` so the foreign key for `SessionStats.SaveSessionStats` exists by the time the first metrics flush fires (previously dropped with `no such row`)

## [0.0.11] - 2026-05-11

### Added
- LLM retry visibility on the wire: every retry the engine schedules emits `card.tick(kind=note)` carrying attempt number, planned backoff delay, classified error type, and HTTP status — front-ends can render "重试中 (N/M, Xms 后再试)" instead of staring at silent waits
- LLM heartbeat events: 30 s keep-alive ticks during in-flight LLM calls propagate up the parent_card_id chain so long-thinking models no longer cause the surrounding step / agent / message cards to orphan-timeout
- First-byte timeout on LLM calls: `llm.first_byte_timeout` (default 120 s) catches "TCP black hole" upstreams that accept the request but never send a chunk; disarms once the first chunk lands so legitimate long thinking preludes are not penalised
- Configurable LLM timeouts via yaml: `llm.api_timeout`, `llm.first_byte_timeout`, `llm.max_retries` exposed as top-level config keys
- Step-decision gate: when retries / replans exhaust, Scheduler and PlanCoordinator emit `prompt.user(kind=step_decision)` so the user picks `continue` / `retry` / `cancel` instead of silently falling back
- Chain-only lifecycle for orchestration tool cards: Specialists and Task tool cards now opt out of the orphan watchdog but stay tracked in the parent chain — descendant heartbeats still refresh ancestors above
- LLM-driven `LLMSubagentResolver` replaces keyword scoring: structured-output tool call picks the executor from `AvailableSubagents` with rationale, falls back to heuristic on nil-provider / LLM-error / out-of-set-pick
- Planner and SubagentResolver route through `retryLLMCall`: both now inherit transport-level retry, heartbeats, retry-status events, and FirstByte/API timeouts that L1 emma and L3 sub-agents already have
- Per-frame WebSocket trace logging behind `channels.websocket.trace_frames` toggle for lifecycle-level debugging without dropping the global log level to DEBUG

### Changed
- Plan card parent is now the emitting Specialists agent card (was the turn card); fixes the topology so writer heartbeats walk writer → step → plan → specialists agent → tool → turn and the Specialists tool card stays alive through the whole plan
- `ping` / `pong` are now top-level wire frames (`{"type":"ping"}` / `{"type":"pong"}`) with no envelope / seq / severity — pure liveness signal, decoupled from `session.event`
- Specialists L2 worker no longer carries a 15-min hard timeout; AgentTool dispatch no longer carries a 5-min hard timeout — long-running plans are bounded by per-call LLM timeouts and the step_decision gate instead of one wall-clock cap that killed legitimate work mid-flight
- SubmitTaskResult `summary` over 200 chars is now truncated with `…` instead of rejected, so the sub-agent isn't forced to redraft when slightly over budget
- LLM retry plumbing uses `retry.Retryer.DoWith(ctx, fn, onRetry)` — per-call observer keeps retry events scoped to one caller's `out` channel even when the Retryer is shared across concurrent sub-agents

### Fixed
- Specialists tool card no longer suffers spurious `orphan_timeout` closes mid-plan: 120 s `CardTool` watchdog used to kill the tool card once the planner stopped tick-ing it (e.g. while writer was running), surfacing "工具失败" while the underlying step succeeded
- DNS / network errors at the L1 main loop now retry via `retry.Retryer` with the same exponential backoff path L3 sub-agents already used, instead of failing on the first attempt
- LLM call ctx cancellation no longer counts as a retryable transient error: a dead ctx propagates through the Retryer as non-retryable so we stop wasting attempts when the parent has already given up
- Wire-frame `card.close` for sub-agent abort no longer maps to `StatusOK`: `"aborted"` now produces `StatusCancelled` (was silently dropped to ok)



### Added
- Roster-agnostic LLM planner: replaces the keyword-rule HeuristicPlanner; the LLM produces a step DAG via the `emit_plan` tool with retry-on-validation, capped at `maxSteps`, and intentionally does not see the available sub-agent list — `SubagentResolver` picks the executor at dispatch time
- Step-level retry on transient failures: scheduler retries up to two attempts on timeout / rate-limit / overloaded / 5xx / `terminal_blocking_limit` / `terminal_model_error` signals before falling back to plan-level replan; `step_started` fires per attempt and `step_completed` / `step_failed` carry cumulative `attempts`

### Changed
- `plan_review` confirmation now matches `prompt.user(question)` semantics: no `card.add(plan)` while waiting, no orphan watchdog, ctx deadline stripped — the user can take as long as they need to review (protocol v0.4.0)

### Fixed
- Tool-result metadata (e.g. WebSearch `urls` / `query` / `result_count`) now flows through the v2.2 translator to `ToolPayload.Metadata` instead of being dropped after promoting `render_hint` / `language` / `file_path`
- Scheduler step failures triggered by terminal sub-agent reasons (`model_error` / `blocking_limit` / `prompt_too_long`) now populate `StepResult.Failures` with `terminal_<reason>: <message>`, log a structured Warn at the scheduler boundary, and feed the retry classifier — previously these failed silently with empty `step_failed` payloads and no server log

## [0.0.9] - 2026-05-07

### Added
- L2 multi-mode coordinator framework: routes Specialists / Orchestrate work through one of pluggable modes (ReAct, Plan), with `Planner`, `Scheduler`, `Judge`, `Escalation`, `Fallback`, `Budget`, `ModeSelect`, and `SubagentResolver` as independently testable components
- Plan-mode user-confirmation flow: when `user.message.plan_confirmation: "required"`, the framework emits `plan.proposed` (carrying the editable step DAG) and blocks until the client returns `plan.response` with approval / edits / rejection; mirrored by `plan.approved` echo (protocol v1.15+)
- Plan / Step lifecycle emit events from the PlanCoordinator path: `plan.created` / `plan.updated` / `plan.completed` / `plan.failed` and `step.dispatched` / `step.completed` / `step.failed` / `step.skipped` (protocol v1.16; envelope/display/metrics are placeholder, payload is fully populated)
- Coordinator-mode threading: `user.message.coordinator_mode` selects ReAct vs Plan per turn; `tool.WithCoordinatorMode` ctx value flows from router → Specialists → SpawnConfig
- LLM call timing breakdown on `llmCallResult` (`firstByteAt` / `lastChunkAt` / `endAt`) for diagnosing gateway hangs, extended thinking, and network buffering separately

### Changed
- Plan-step `skill` field renamed to `subagent_type` and `available_skills` to `available_subagents` to disambiguate from `AgentDefinition.Skills` (capability tags); `subagent_type` is now optional, with `SubagentResolver` picking the executor at dispatch time
- emma's task-dispatch principles now require an explicit clarification-merge self-check before any Specialists spawn — original-request plus user-clarification answer must be combined into the task string

### Fixed
- Bifrost adapter no longer hangs ~6m40s after the model finishes streaming: `consumeStream` returns as soon as a chunk carries `FinishReason`, instead of waiting for the upstream chunk channel to close on its own (which only happens at the underlying HTTP idle timeout)
- AskUserQuestion tool description gained an explicit reminder that the user's clarification answer must be folded into the next task / prompt — previous wording let the LLM forward the original (un-clarified) request

## [0.0.8] - 2026-05-07

### Added
- v2.2 WebSocket protocol: UI-first card model with 8 actions (card.add/set/append/tick/close + prompt.user/reply + session.event) over 12 card_kinds, unified `ErrorInfo`, registry-driven `Hint` defaults, orphan watchdog, and `artifact://` URI as a protocol-level hard constraint
- New `internal/emit/v2` package — Builder API, lifecycle tracker, per-trace sequencer, sink abstraction, artifact rewrite
- Fault recovery: unanswered `permission` / `question` / `plan_review` prompts persist to a SQLite `pending_waits` table and replay (same `request_id`) when the client reconnects to the same session after a server restart
- `Prompter` + `WaitStore` + `TextResumer`: server writes the wait before any wire frame leaves, restart-survivor reply path falls back to SQLite, hourly TTL janitor (15-day retention) reclaims abandoned waits
- Per-session debounced persist worker: mutations within 500 ms collapse into a single disk write
- WebSocket protocol specification: `docs/protocols/websocket.md` with reconnect semantics, recovery flow, and a 6-item client implementation checklist

### Changed
- Default WebSocket endpoint path is now `/v1/ws` (was `/ws`)
- WebSocket channel completely rewritten on top of v2.2; engine `EngineEvent` flow runs through a new translator that maps to emit v2 calls
- Session manager owns one persist worker per session; `Manager.Shutdown` flushes all workers

### Fixed
- `database is locked (SQLITE_BUSY)` under streaming load: SQLite DSN now sets `busy_timeout(5000)` / `journal_mode(WAL)` / `synchronous(NORMAL)`, and the pool is pinned to a single connection so writes serialise on the `database/sql` mutex instead of contending at the file lock

### Removed
- Legacy v1 WebSocket implementation: `connection.go`, `mapper.go`, `protocol.go`, `registry.go`, and their tests

## [0.0.7] - 2026-05-05

### Added
- Three-tier agent architecture: emma (L1, user-facing) → Specialists (L2, coordinator) → workers (L3, leaf executors), with strict per-tier tool filtering and prompt isolation
- `TierSubAgent` contract on `AgentDefinition`: every L3 worker declares `OutputSchema`, `InputSchema`, `Limitations`, `ExampleTasks`, `CostTier`, and `Temperature`; `Tier` routes spawns to the strict L3 driver
- `runSubAgentDriver`: leaf-only ReAct loop with self-check, nudge cap (3x), reject cap (3x), and SubmitTaskResult-or-EscalateToPlanner termination
- 7 redesigned built-in workers (`writer`, `researcher`, `analyst`, `developer`, `travel_planner`, `recommender`, `scheduler`) as pure functional L3 with specialized `SystemPrompt` and per-worker schemas
- `SubmitTaskResult` tool with server-side validation (lineage, temporal window, role/type/mime, OutputSchema)
- `EscalateToPlanner` tool: L3 needs-planning escape hatch (paired with SubmitTaskResult)
- `SpawnConfig.Inputs` + InputSchema validation in `SpawnSync` — validated before any LLM call
- Artifact subsystem with task contract: SQLite-backed store at `~/.harnessclaw/db/artifacts.db`, `ArtifactRead` / `ArtifactWrite` tools, available-artifacts preamble auto-injected on sub-agent spawn, TTL janitor, parent-version chains
- WebSocket forwarding of artifact refs and L3 sub-agent task/intent for richer client observability (protocol v1.12)
- Plan-based DAG executor for `Orchestrate` tool with `PlannerListing` rich-metadata routing
- Structured lifecycle event protocol via `internal/emit` package
- `AskUserQuestion` tool for clarification flows; `WebSearch` updates
- `SafetyLevel` classification (safe / caution / dangerous) on every built-in tool, with `WithoutDangerousUnless` filter for L3 pools
- Minimal in-house JSON-schema validator (`ValidateAgainstSchema`) shared by SubmitTaskResult and InputSchema enforcement
- `BuildFunctionalIdentity` for L3 workers — team-free, persona-free identity that does not leak emma's L1 prompt

### Changed
- Worker system prompts no longer inject team affiliation or personality for `TierSubAgent`; identity becomes purely functional
- `lifestyle` worker split into single-responsibility `travel_planner` and `recommender`
- Specialists / Orchestrate consume structured `PlannerListing` for richer routing decisions
- All user-facing tool descriptions translated to Chinese for consistency
- emma identity phrasing tightened in the L1 prompt
- Artifact subsystem redesigned around the task contract (producer task_id stamping, expected-outputs render block)

### Fixed
- `ArtifactRead` defends against `artifact_id` hallucination: detects UUID-shaped fabrications (8-4-4-4-12 dashed and 32-hex compact) and instructs the LLM to escalate instead of retry, preventing the retry-then-fabricate loop that previously consumed long stretches of LLM time
- Artifacts guidance section now shows the real ID format example and explicit "don't know an ID? EscalateToPlanner" instruction

### Removed
- `Personality` field is no longer rendered into TierSubAgent system prompts (kept on the definition for team-table metadata only)

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
