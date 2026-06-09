# Changelog

All notable changes to this project will be documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/), and versions are published to GitHub Releases.

## [Unreleased]

## [0.0.21-beta.1] - 2026-06-09

### Fixed
- Image generation requests now use a longer HTTP/TLS budget and bypass the generic tool executor timeout, so slower image providers can complete instead of failing after the default tool deadline.

## [0.0.21-beta.0] - 2026-06-09

### Added
- Image generation requests can now run through the engine `image_generate` tool using configured image-generation providers.
- `agent.image_generation` is now separated from chat fallback routing so image-only models do not enter the text conversation provider chain.
- Provider snapshots now expose backend-resolved image generation target URLs for the desktop client.
- Added a local image generation proxy and mock server for manual image workflow testing.

### Fixed
- Browser Agent now defaults to enabled in fresh engine configs, matching the packaged desktop sidecar flow.

## [0.0.18] - 2026-06-04

### Changed
- Browser Agent now resolves its native binary only from `CLAUDE_TOOLS_BROWSER_AGENT_BINARY_PATH` or the packaged sidecar next to the engine executable. YAML `tools.browser_agent.binary_path` is no longer a runtime input, so stale local config cannot redirect packaged apps to a bare `agent-browser` command on `$PATH`.

### Fixed
- Packaged desktop launches can no longer be short-circuited by old configs that set `binary_path: "agent-browser"`; the locked sidecar binary in the runtime bundle remains authoritative.

## [0.0.17] - 2026-06-04

### Added
- Added engine-owned Browser Agent runtime bundling: release builds now publish `harnessclaw-engine-runtime-<platform>-<arch>.zip` assets containing the engine binary, the pinned `agent-browser` native binary, and a runtime manifest.
- Embedded the Browser Agent skill, references, and templates into the engine via Go resources so the desktop app no longer carries Browser Agent skill files.
- Added explicit Browser Agent binary path configuration through `tools.browser_agent.binary_path` and `CLAUDE_TOOLS_BROWSER_AGENT_BINARY_PATH`.

### Changed
- Browser Agent binary discovery now prefers the configured path and then the sidecar binary next to the engine executable, matching the packaged runtime layout.

## [0.0.16] - 2026-06-03

### Added
- Endpoint config now carries an optional `group` display tag (yaml + POST/PATCH/GET). Used by the desktop client to bucket models by series in the Settings UI. Engine ignores the field for routing.
- Image generation model catalog support: `image_generation` is now a known model capability token, appears in derived model capabilities, and can be persisted through provider endpoint `model_type` overrides.
- Default registry entries for OpenAI GPT Image (`gpt-image-2`) and Doubao Seedream image-generation models, including provider endpoint metadata for image generation APIs.
- `provider.ChatRequest.Purpose` label disambiguates main-loop vs compactor-summarize vs other call sites in bifrost dial logs (`purpose=main_loop` / `purpose=compact_summary`).
- Bifrost adapter dumps the full `BifrostError` struct plus a shape-only summary of the outgoing request (per-message role / block types / empty markers) whenever upstream returns 4xx, so request-body bugs that trigger HTTP 400 are diagnosable from a single log line.
- Bifrost stream consumer publishes a per-stream chunk histogram (`chunks_text_delta` / `tool_call_delta` / `reasoning_delta` / `finish` / `usage_only` / `other`) on `MessageEnd` and warns when `output_tokens > 0` but no forwarded channels saw any payload.
- Loop pre-flight scan flags four request-body pathologies (empty text block, message with zero content blocks, first message not user, orphan tool_result) before sending — emits a single WARN with index/role/kind for each issue.
- Loop warns when `buildAssistantMessage` produces a message with zero content blocks and DEBUG-traces `Message.Tokens` vs raw usage so compactor false-triggers are diagnosable.
- LLMCompactor logs the full lifecycle (`compact.should` / `compact.begin` / `compact.summary` / `compact.end`) plus circuit-breaker state changes; summarize requests are tagged `Purpose=compact_summary`.
- `ContractEnforcerWithLogger` records every branch decision (no-tool-call nudge, budget-exhaustion hard nudge, validation retry, terminate-on-valid-submit) at INFO; freelancer wires its logger so "why did this agent get the '2 turns left' message?" is answerable from logs alone.
- `subagent_end status=failed` card.close payload now carries an `ErrorInfo` block built from `Terminal.Reason/Message/Turn` so clients render the actual failure cause instead of an opaque "failed" badge.
- emma `ProcessMessage` now uses `context.WithCancelCause` and attaches typed cancel causes (`errEmmaAborted` on `AbortSession`, `errEmmaSessionEnded` on normal goroutine exit); the runner logs `ctx_cause` alongside `ctx_err` when an LLM error surfaces so operators can tell user-abort vs session-end vs upstream-cancel apart in a single line. `AbortSession` also logs at INFO with the cause-aware flag.
- `cmd/ws_smoke`: one-shot WebSocket smoke client that dials the channel, opens a session, posts a single user message, streams frames until the turn closes (or a 90s budget elapses), and prints a per-frame-type tally — used to verify the L1→L2→L3 chain without the Electron UI.
- `filewrite` tool caps `content` at 25k characters via JSON-schema `maxLength`, rejecting oversized writes at the schema layer before the tool body executes.
- `fileread` tool sniffs leading bytes for binary magic (PE / ELF / Mach-O / image headers + null-byte heuristic) and refuses to pull binary blobs into the LLM context.
- Browser Agent: client-routed browser tools wired end-to-end with an explicit browser-session lifecycle so the engine spawns / reuses / closes browser sessions per sub-agent invocation.
- Scheduler L2 react palette gains `bash` so the L2 agent can run shell commands directly when its plan requires it, instead of always hopping through an L3 freelancer.
- `agent/common` prepends a workspace prelude to every sub-agent's first user message, telling it the workspace root, available output paths, and `meta.json` contract up-front.
- L3 freelancer surfaces residual workspace files in `meta.json` output and tightens the per-turn / total budget defaults to curb runaway loops.
- Tool executor wraps `max_tokens`-truncated tool input with an actionable hint so the LLM can self-correct on the next turn instead of failing silently on a half-parsed argument.
- `ToolTimeout` is plumbed from engine config through to the tool executor and propagated into sub-agent loops, so per-tool deadlines actually take effect end-to-end.
- `AbortSession` aborts every pending `Awaits` (tool / perm / plan / step-decision) via `Awaits.AbortAll`; a session abort no longer leaves goroutines parked on closed channels.
- Loop dumps per-turn LLM request shape (message-count / role pattern / content-block kinds) so sub-agent failures are diagnosable from logs alone.
- emma forbids exploratory `freelance` dispatches at L1; exploratory work is delegated into the L2 scheduler which spawns explore sub-agents internally.

### Changed
- L2 scheduler module (`internal/engine/agent/scheduler`): both `react` and `plan` modes now delegate to the v3.1 scheduler kernel (`enginesched.Coordinator` → `Scheduler.Submit` → `dispatch/{react,plan}`). The previous in-module react LLM loop is gone — react fires a single freelancer leaf via `SpawnAndWaitOne` and composes `SpawnResult.Output` from the meta.json the leaf wrote, matching the plan path. `Deps` slimmed to `Logger` / `SessionMgr` / `RootDir` / `WorkspaceRoot` / `Coord` (Provider / Registry / Compactor / Retryer / PromptBuilder / MaxTokens / ContextWindow / ToolTimeout / LLM timeouts / DefRegistry / Spawner removed — they now live inside `Coord`'s `QueryEngineFactory`). Both modes return an explicit `requires Deps.Coord` error when Coord is nil instead of nil-deref panicking.

### Fixed
- LLMCompactor no longer constructs an `assistant{text:""}` block when summarize returns an empty/whitespace-only string — falls back to `microCompact` and increments the circuit-breaker count, preventing the empty-block from triggering an HTTP 400 on the next turn.
- LLMCompactor synthetic summary is now a `user`-role message with a `[Prior conversation summary]\n` prefix instead of `assistant`, so Anthropic's "first non-system message must be role=user" constraint holds when the original leading user turn is summarized away.
- LLMCompactor summarize prompt explicitly tells the model "Reply with the summary text only. Do not call any tools.", reducing the chance of completion tokens landing on a non-text channel that yields an empty summary.
- Browser Agent now serializes top-level browser tasks, binds command CDP targets to the current browser session, and rejects cross-agent endpoint reuse to avoid multi-window race conditions.
- `agent/loader.LoadFromDirectory` blocks path traversal: agent-definition imports that resolve outside the agents root are rejected before any read (security fix, #31).
- Browser Agent sub-agent inherits the parent's internal tool approvals so it can call MCP / workspace tools without re-prompting.
- Browser Agent supports direct result submission: the browser sub-agent can call `submit_result` to terminate without an artificial trailing tool call.
- `llmcall` guards malformed `tool_call` JSON from upstream and isolates the bifrost Chat dial from the watchdog goroutine, so a stuck Chat call no longer holds the watchdog timer.
- bifrost provider sanitizes truncated `tool_use` arguments (closes unbalanced braces / quotes) before forwarding, keeping the stream alive when upstream truncates mid-token.
- bifrost adapter strips mid-stream unanswered `tool_calls` and drops orphan `tool` messages before sending the next request, so a previous turn's incomplete tool round doesn't HTTP-400 the follow-up.
- LLM API and first-byte timeouts propagate from engine config into sub-agent loops; a runaway sub-agent honours the same deadlines as the main loop.
- `agent/common` pre-creates each task's workspace directory before the sub-agent's first write, so the LLM's first `filewrite` doesn't fail on a missing parent dir.
- Workspace bootstrap is deferred to L2: emma-only queries (no L2 dispatch) leave no disk footprint, eliminating empty `tasks/` directories from short turns.
- Compactor skips orphan `tool_result` blocks at the compaction boundary so the compacted history never starts with an unanswered tool result.
- emma pins freelancer `MaxTokens` to 8192 and propagates `ToolTimeout`, so spawned L3 leaves use a sensible per-turn budget instead of inheriting an unset zero.
- `toolpool`: `AllowedTools` whitelist now bypasses the `AgentType` blacklist, so an explicit per-agent allow-list always wins.
- L3 freelancer is blocked from calling `orchestrate`; the tool-name leakage in freelancer principles is removed so the LLM stops attempting it.
- Scheduler isolates its teaching example inside `<example>…</example>` tags so the model stops hallucinating the example's payload into real plans.
- Scheduler uses `AgentDefinition.AllowedTools` as the L2 react palette whitelist, matching the rest of the tier-resolution path.
- Sub-agent stream events are routed per agent id across translator / llmcall / toolexec, so concurrent sub-agents no longer cross-contaminate frame streams.
- L3 card nesting: agent-tool propagates parent agent id from ctx so L3 cards render under their parent L2 card instead of the root.

## [0.0.15] - 2026-05-26

### Added
- L2 scheduler kernel (v3.1) under `internal/engine/scheduler/`: top-level `Scheduler.New` + `Submit` + `Start` wires bus, kernel, dispatch strategies, and runtime handlers (`onSpawn` / `onResult` / `onLifecycle` / `onTerminal` / `onExpire` / `onCancellingDrained` / `onCompletedFromStaging`) into a single message-driven L2
- 8-state task state machine in `scheduler/tstate`: `Kernel` with `RollbackAdmit` + epoch guards, `Reader` / `Writer` / `StagingWriter` interfaces (R2/R4/R10), in-memory + SQLite `Store` implementations sharing the sessions `*sql.DB`, named-field UpdateField CAS path
- Dispatch strategies in `scheduler/dispatch`: `react.Strategy` spawns one leaf via `SpawnAndWaitOne` (R1/R6 Subscribe-before-Publish); `plan.Strategy` runs the planner-agent + plan-executor-agent two-phase LLM pipeline, with `PlanJudge` rule-tier validation and `FallbackAggregator` graceful degradation
- Routing layer in `scheduler/router`: `HeuristicKindSelector` (react vs plan, mode-select heuristics) + `HeuristicAgentResolver` (keyword-scored sub-agent selection); both pluggable via `Coordinator` config so the project can swap in LLM-based selectors later
- `scheduler/runtime/host`: `StartStrategyHost` forks G1 + G2, stages refs, publishes lifecycle, runs a 3-pass reaper scan (lease / deadline / cancelling) with notify-on-expiry
- Msgbus (`internal/msgbus`): in-process `Bus` with `Publish` / `Subscribe` / `SubscribeOnce` / `Dequeue` / `Ack` / `Nack`, six message kinds (`lifecycle` / `control` / `agent.msg` / `notify` / `task` / `result`), six typed payloads, in-memory + SQLite `Store` implementations with reaper requeue and per-kind typed-struct revival
- L1 (`internal/engine/emma`) and L2 (`internal/engine/scheduler`) now sit in dedicated subpackages; new public accessors on `QueryEngine` — `ApplyMainAgentConfig` / `Config` / `PromptProfile` — let emma cross the package boundary without touching private fields
- L2 dispatch tool `scheduler`: emma calls `scheduler(task)` to enter the L2 layer; `coordinator-mode` (react / plan) flows via `tool.WithCoordinatorMode` on the parent context, never via emma's input schema (ops-only knob, D-mode auto-escalation stays internal)
- `plan_read` tool: read-only access to `plan.json` for plan-executor-agent; TDD-built with full coverage
- Plan-mode profiles + agent definitions: `plan-agent` writes the task breakdown to `plan.json`, `plan-executor-agent` reads it back, dispatches step tasks via `freelance`, updates status; profiles include principles tailored to write/read responsibility split
- `SubagentType` field on `spec.TaskSpec`: explicit agent pinning for plan-mode steps so the planner can request specific roles instead of relying on resolver heuristics
- `EscalationInfo` on `TaskSpec` + transient failure reason constants (`timeout` / `rate_limit` / `overloaded` / `network`) with `Retryable` accessor for the react → plan D-mode escalation context
- `cmd/test/` directory consolidates all local-only e2e probes (`ask_e2e` / `emit_e2e` / `l3_e2e` / `metrics_e2e` / `plan_e2e` / `sched_e2e` / `subagent_e2e` / `tool_catalog`); the entire subtree is gitignored — only `cmd/server/` ships in version control

### Changed
- L1 → L2 → L3 layering becomes visible in the directory tree: `internal/engine/` now exposes `emma/` (L1), `scheduler/` (L2 kernel + dispatch + coordinator), `worker/` (L3 placeholder), `loop/` (cross-cutting query-loop helpers — `SkillTracker` / retry context / drain channel)
- L2 dispatch tool renamed from `task` to `freelance` (clearer intent: emma delegates a single piece of work to an L3 freelancer; multi-step orchestration uses `scheduler` instead)
- Specialists tool renamed to `scheduler` with lowercase tool names project-wide (LLM-facing identifiers normalised)
- Per-role principles split into `internal/engine/prompt/principles/{emma,specialists,worker,explorer,planner,plan-agent,plan-executor-agent}`; per-role packages replace the previous monolith so adding a new role no longer touches unrelated principle text
- `SchedulerCoordinator` renamed to `scheduler.Coordinator` after moving into the scheduler subpackage (the `Scheduler` prefix was redundant inside the package)
- `internal/engine/orchestrate` moved to `internal/engine/scheduler/legacy/` and marked DEPRECATED — Phase-1 plan executor still backs the `Orchestrate` tool until parity with the new plan strategy is reached
- Coordinator-tier `SpawnSync` now routes through `Coordinator.Run` (traffic cutover): emma's `scheduler` tool hits the new scheduler internals by default; the engine wiring deletes the obsolete `coordinator_*.go` files (Phase 3 migration complete)
- `QueryEngine` exposes only the three accessors the L1 wrapper needs; `MainAgentProfile` / `MainAgentDisplayName` / `MainAgentAllowedTools` / `MainAgentMaxTurns` are applied through `ApplyMainAgentConfig` instead of direct field assignment
- L2 / L3 principles now instruct sub-agents to use scheduler-provided workspace tools (Promote / ArtifactWrite) instead of `Bash mkdir / mv / cp`; D13 path-scope boundary documented in the Bash tool description
- `cmd/` reorganised to `cmd/server/` (production) + `cmd/test/` (local probes); `.gitignore` collapsed to a single `cmd/test/` rule with no exceptions

### Fixed
- Plan-mode E2E: tool stripping and session_id propagation for `plan-executor-agent` so spawned step tasks see the parent session and the executor's tool palette is correctly narrowed
- `metaRefToLoopResult` reads `meta.json` to surface the L3 summary back to the L1 caller — previously the summary was lost when the engine routed through `Coordinator.Run` instead of the legacy direct path
- `scheduler/tstate.RenewLease` runs `InTx`, surfaces cancel cascade errors, validates epoch, and references the lease column via a named field constant (no more silently dropped lease updates)
- `msgbus/store` SQLite revives `Payload` as the concrete typed struct per `Kind` so queue consumers can rely on type assertions instead of map-fishing through `any`
- `metatool` derives `task_id` + `agent` from context and stat-fills output bytes; `emit/v2` persists parent links across `Close` for ancestor heartbeat continuity
- `server/bifrost` resolves provider quirks by YAML key first (matches user-visible config) instead of by manifest name
- `translator` clears `toolNames` + `toolsFromPlanning` on `ToolEnd` so subsequent cards do not inherit stale state from a previous tool's lifecycle

### Removed
- `internal/engine/coordinator_*.go` files (Phase 3 migration): `coordinator_judge.go` / `coordinator_fallback.go` / `coordinator_subagent_resolver.go` / `coordinator_mode_select.go` / `coordinator_scheduler.go` etc. — their responsibilities are now owned by `scheduler/dispatch/{plan,react}` (judge + fallback aggregator) and `scheduler/router` (kind selector + agent resolver)
- `cmd/test/` content is no longer tracked in git: `tool_catalog/main.go` and `metrics_e2e/main.go` reverted to local-only status (previously partially tracked); local copies preserved on disk for ad-hoc runs

## [0.0.14] - 2026-05-19

### Added
- Multimodal user input (image + PDF) end-to-end (`internal/engine/multimodal`): typed `IncomingContentBlock` parser + size-capped builder (per-block / per-message base64 limits, see `MaxBase64BlockBytes` / `MaxTotalBytesPerMessage`), capability `Gate` enforced at the router before engine dispatch, deterministic `UnsupportedModalityError` with user-facing message + rejected-modality list, redactor for log-safe payload previews
- Per-endpoint capability override: `endpoint.model_type` yaml field (`vision` / `pdf` / `audio` / `video` / `reasoning` / `tools` / `search`) overrides the manifest baseline `SupportsFlags`; unknown tokens warn-and-drop at startup, rejected with 400 on `PATCH /api/v1/providers/{p}/endpoints/{e}` (`*[]string` semantics: omitted = leave alone, `[]` = clear override and revert to manifest)
- Fallback-chain capability intersection: `Manager.ChainSupports` AND-intersects `SupportsFlags` across primary + every fallback entry so the multimodal gate rejects inputs that would fail mid-chain on fail-over (correctness over availability — "switch model" upfront beats an opaque 400 from a fallback hop)
- `GET /api/v1/agent/capabilities` endpoint: serves the resolved active-model `SupportsFlags` + derived capability buckets (`multimodal` / `tools` / `reasoning` / `search`) using the same bridge the gate uses, so the client can never disagree with the server about what's allowed
- `capabilities` array on `/api/v1/models` responses: collapsed bucket list derived from `SupportsFlags` for ergonomic UI chip rendering (granular flags remain authoritative for per-feature gating)
- Bifrost adapter image / file block conversion: typed `ContentTypeImage` / `ContentTypeFile` blocks rendered as Anthropic / OpenAI Vision payloads via `data:` URL synthesis from base64 + media_type, or pass-through for remote URLs; Anthropic ephemeral `cache_control` breakpoints added to image / PDF blocks with post-conversion `capImageCacheBreakpoints` clamp to the 4-breakpoint per-request limit (oldest dropped first)
- L3 freelancer execution mode: skill lifecycle tools (`loadskill` / `unloadskill` / `searchskill` / `listloadedskills`) with per-session `skill_tracker` context propagation, `skill_block` engine event surfacing loaded-skills state to UI, freelancer hydration of agent definition + prompt sections (skills section + principles text)
- Artifact blob store: filesystem half at `~/.harnessclaw/artifact-blobs` backing large binary artifacts alongside sqlite metadata; new `/api/v1/artifacts` HTTP handler for client download
- `ArtifactWrite` source_path allow-list: pinned reads to `~/.harnessclaw/workspace` + configured `skills.dirs` (no arbitrary filesystem ingest by the LLM)
- Sub-agent token attribution: `sessionstats` distinguishes immediate parent vs root session buckets so multi-agent dispatches roll up to the right conversation; L3+ sub-agents dual-write LLM and tool stats to the root session tracker
- Tools management API (`/api/v1/tools`): GET (list + per-tool config) + PATCH (per-tool config hot-swap with yaml rollback) backed by `tool.Registry.Replace` for atomic adapter swap; `config/persist.SetToolConfig` writes `tools.<name>.*` yaml blocks while preserving comments / key order
- `CardSystem` card kind in `emit/v2` (icon=info, role=system, untracked) + `SystemPayload.topic` field for framework-level notices independent of card lifecycle; `EngineEventSystemNotice` event + `SystemNotice` payload type
- `SearchGapDetector` (`internal/engine/capability_gap_detector.go`): detects when a user asks for web search but no search tool is enabled, emits a session-deduped `card_kind=system` notice pointing at the settings page; wired into `QueryEngine` and sub-agent spawn step 6
- iFlyTek Spark v2/search Bearer-token API: WebSearch tool migrated to the new Spark search v2 endpoint with Bearer auth
- `error.unsupported_modality` error type + structured `details` map on `ErrorInfo` so the wire format can carry `model` / `rejected_modalities` / `user_message` / `error_code` to the client in a single error frame
- WebSocket per-frame read limit raised to 32 MB to accommodate multimodal `user.message` frames; wire-layer size-cap check rejects oversized payloads with `payload_too_large` before they reach the engine

### Changed
- `pkg/types.ContentBlock` extended with multimodal fields (`MediaType` / `Data` / `URL` / `Path` / `Filename` / `Size`); zero-valued + `omitempty` keeps text-only payloads byte-identical on the wire
- `router.New` signature now takes a `ModelInfoProvider` for capability gating; `nil` disables gating (used by older tests); production wires a bridge from `provider.Manager.ActiveModelKey` + registry `LookupModel` + endpoint-override merge
- `Emma` principles tightened: long user requirements must be passed verbatim to Specialists — task field can't shrink N-item lists to one item, can't add reverse constraints the user didn't state, can't pre-split into "first read, then implement" steps
- `internal/artifact/sqlite_store` applies the same pragmas as the sessions store (`busy_timeout=5000` / `journal_mode=WAL` / `synchronous=NORMAL`) so concurrent sub-agent writes no longer hit `SQLITE_BUSY`
- `llm.call.stream_stuck` log line downgraded from WARN to INFO (upstream slowness is not a server-side defect; the retry budget handles it on its own — WARN was inflating monitoring alerts)
- `agent.Message.ParentMessages` semantics documented: text-only by design; if extended to carry typed content blocks, callers MUST re-run `multimodal.Gate` against the sub-agent's resolved model (else a text-only sub-agent silently receives image data and fails at the provider)
- Bifrost adapter `convertMessages` applies `capImageCacheBreakpoints` post-conversion clamp (per-block `cache_control` is added eagerly; the global cap is enforced once at the end)

### Fixed
- L2 sub-agent loop swallowed `EngineEventSystemNotice`: the L2 forward-switch whitelist in `subagent.go` was missing the type, so any system notice (e.g. `SearchGapDetector` firing) emitted under an L2 dispatcher never reached the WS translator — added to the pass-through case list
- Team sub-agents lost web search when `TavilySearch` was disabled: `AllowedTools` now lists `WebSearch` as a fallback so the agent doesn't silently lose the capability when credentials are missing for the primary search provider
- `/api/v1/tools` PATCH could race on concurrent updates: mutex now guards `cfg` mutation; empty-cfgPath errors surface to the caller instead of silently no-op'ing; rollback failures get logged

### Removed
- N/A

## [0.0.13] - 2026-05-15

### Added
- Multi-provider failover dispatcher (`internal/provider/failover`): four-tier RetryPolicy (Probe 5s / Fast 15s / Medium 30s / Full) plus three-state per-provider health (`healthy` / `tripped` / `ready_to_probe`) with exponential cooldown backoff; classifies which errors cross provider boundaries and which stay local (prompt_too_long / max_output_tokens / ctx.Canceled never trip the provider)
- Hot-swappable provider manager (`internal/provider/manager`): atomic.Pointer wraps the live Failover dispatcher so chain mutations don't disturb in-flight calls; adapter cache reuses bifrost adapters across chain-only mutations
- Top-level `agent:` yaml block replacing `llm.fallback_chain`: `primary` (single dotted ref `"provider:endpoint"`), `fallback_chain` (ordered backups), `max_tokens` / `temperature` (adapter-baked defaults, unified [0,1] temperature scaled per provider type — anthropic ×1, openai/gemini ×2), `context_window`, `max_turns` (moved from `engine.max_turns`), `max_tool_calls` (0 = unlimited), `thinking_intensity` (`low` / `medium` / `high` / blank)
- `endpoint.context_window` field per endpoint declares the model's intrinsic context limit; `Manager.EffectiveContextWindow()` returns `min(agent.context_window, primary_endpoint.context_window)` with 200_000 fallback; surfaced as `effective_context_window` in `GET /api/v1/agent` and the `engine initialized` startup log
- `GET` / `PATCH /api/v1/agent` endpoint (replaces `/api/v1/fallback-chain`): partial-updates any subset of agent fields, validates `max_turns ≥ 1`, `max_tool_calls ≥ 0`, `thinking_intensity` enum
- `POST /api/v1/providers` runtime API for creating new provider entries without restart; `gemini` added to `type` whitelist alongside `openai` / `anthropic`
- Comment-preserving yaml persistence (`internal/config/persist`): `yaml.Node`-based mutator rewrites only changed keys, preserving inline comments / key order / hand-tuned indentation across PATCH-driven persistence cycles
- Per-call token usage on `llm.call ok` log: `input_tokens` / `output_tokens` / `cache_read` / `cache_write` / `thinking_tokens` now at INFO level (was DEBUG-only via `bifrost stream MessageEnd`)
- Bifrost stream lifecycle DEBUG logs: `llm.call.dial` (before SDK call) / `llm.call.connected` (after stream returned) / `llm.call.stream_closed` (when wrapper goroutine exits) — a hang can now be located between dial / connected / streaming / closing
- Stream-stuck WARN watchdog: `doSingleLLMCall` emits `llm.call.stream_stuck` every 30 s of no new chunk; observability only — `firstByteTimeout` / `apiTimeout` still own hard cancellation
- Provider endpoint `disabled` field (cascading with `provider.disabled`): effective disabled = `provider.Disabled OR endpoint.Disabled`; auto-removed from chain on PATCH `disabled:true`, chain auto-fills with first enabled endpoint when empty

### Changed
- `agent.max_tokens` / `agent.temperature` baked into each bifrost adapter as defaults; `ChatRequest.Temperature/MaxTokens == 0` falls back to these. `PATCH /api/v1/agent` on these fields invalidates the adapter cache so subsequent calls pick up the new defaults
- `ChatRequest.ContextWindow` field added as observability hint (not sent to vendor); `stats classify.limit` now reads it instead of `req.MaxTokens` — dashboard `ContextWindow.Limit` correctly reflects configured budget instead of response cap
- `Manager.AdapterBuilder` signature now takes `agent config.AgentConfig` so adapters can resolve effective temperature / max_tokens at build time
- `emmaPrinciples` trimmed from 8142 → 3824 chars (−53%): removed redundant repetition, anti-pattern lists rewritten as positive instructions, reordered for LLM head-attention bias; `TestPrinciplesSection_*` invariants kept intact
- Manager `AgentSnapshot` payload now includes `effective_context_window` so operators can compare configured vs. capped value
- Provider `type` field is now required (was optional with implicit default); empty / unknown types dropped at startup with WARN, not FATAL — server stays bootable with valid providers when one entry is malformed
- Empty fallback chain and bad yaml entries now warn-and-drop instead of FATAL: server enters degraded mode (Chat returns `ErrNoEndpoint`, management API stays mountable) until operator PATCHes a valid agent config

### Fixed
- `GET /api/v1/sessions/{id}/metrics` returned empty `sub_agents`: `executor.go` never injected `ToolUseContext` before `t.Execute`, so Specialists read `parent_session_id=""`, short-circuited `StartSubAgent`, and bypassed `stats_provider` entirely — Specialists + all L3 sub-agent token spend was invisible. Fix injects `ToolUseContext` (SessionID / ToolCallID / ToolName / ToolInput) at the executor boundary
- Task tool rejected legitimate sub-agent types (`writer` / `researcher` / `analyst` / `developer` / `lifestyle` / `scheduler`): hardcoded 3-entry whitelist in `agenttool/input.go` contradicted the tool description, costing one wasted LLM round-trip per Specialists dispatch (~6.5k token / 6 s). Validate now defers name resolution to the `defRegistry` at spawn time
- Orchestration tool cards (Specialists / Task) false-positive `orphan_timeout` on the `EngineEventToolCall` path: only `EngineEventToolStart` had the `isOrchestrationTool` check appending `WithoutLifecycle()`; client-side tool calls were still subject to the 120 s `CardTool` watchdog, causing UI to show "工具失败" while the underlying multi-minute plan was healthy. Both paths now opt out
- `QueryEngineConfig.MaxTokens` was overloaded as both "response cap" and "context budget" — compactor was misclassifying agent.max_tokens (e.g. 2048) as the context window, triggering auto-compact at trivial token counts. Split into distinct `MaxTokens` (response cap) and `ContextWindow` (compactor budget) fields

### Removed
- `/api/v1/fallback-chain` endpoint (replaced by `/api/v1/agent`)
- `llm.fallback_chain` yaml field (migrated to `agent.fallback_chain`); `persist.SetAgent` strips the legacy key on first save
- `engine.max_turns` yaml field (moved to `agent.max_turns`)
- Hardcoded `200_000` literal for prompt-context window size in `internal/engine/queryloop.go` and `subagent.go` — now derived from `qe.contextWindow()`

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
