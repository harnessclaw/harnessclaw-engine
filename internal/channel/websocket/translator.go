package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/internal/engine/wait"
	"harnessclaw-go/internal/toolphrase"
	"harnessclaw-go/pkg/types"
)

// Translator converts existing engine `*types.EngineEvent` into v2.2
// emitv2 Builder calls. This is the bridge that keeps the engine's
// emit-site code unchanged while the wire format upgrades to v2.2.
//
// Per-session state (turnCardID / messageCardID / open tools / sub-agent
// emitters) is kept in a session map so concurrent sessions don't
// stomp on each other.
//
// Lifecycle assumption: the engine emits events in a deterministic
// order per session. The translator is single-threaded per session by
// the caller (channel.SendEvent invocations are serialised by the
// upstream queryloop).
type Translator struct {
	mu       sync.RWMutex
	sessions map[string]*sessionState

	// recovery: when set, every prompt.user emission is persisted via
	// Prompter.Issue BEFORE the wire frame goes out. Optional; nil
	// disables persistence (the in-memory askQuestion/pendingPlan/
	// pendingPerm maps are still authoritative for live answers).
	prompter promptIssuer

	// phrasePicker resolves localized strings for tool card phase hints
	// and the M4 message card hint. Injected; nil = degraded mode where
	// PhaseHint stays empty and front-end falls back to phase-enum
	// defaults.
	phrasePicker *toolphrase.Picker
}

// promptIssuer is the minimal slice of prompter.Prompter the translator
// needs. Defined as an interface so tests can inject a fake without
// pulling in the full prompter package.
type promptIssuer interface {
	IssueWait(ctx context.Context, w wait.PendingWait) error
}

// NewTranslator constructs a Translator. picker may be nil for
// degraded mode (PhaseHint stays empty; front-end uses Phase enum
// defaults).
func NewTranslator(picker *toolphrase.Picker) *Translator {
	return &Translator{
		sessions:     make(map[string]*sessionState),
		phrasePicker: picker,
	}
}

// SetIssuer wires recovery persistence. When set, the translator saves
// every prompt to the wait store before emitting the wire frame.
func (t *Translator) SetIssuer(p promptIssuer) {
	t.mu.Lock()
	t.prompter = p
	t.mu.Unlock()
}

// sessionState is the per-session translation state. Reset on EngineEventDone.
type sessionState struct {
	mu sync.Mutex

	// emitter is the root Emitter for this session. Captured on the first
	// Translate call so Resolve* helpers (which don't carry an em
	// argument) can still talk to the lifecycle tracker for
	// suspend/resume.
	emitter *emitv2.Emitter

	turnCardID    string
	turnNo        int
	messageCardID string
	tools         map[string]string          // tool_use_id → tool card_id
	subagents     map[string]*emitv2.Emitter // agent_id → child Emitter (sub-agent scope)
	subAgentCard  map[string]string          // agent_id → agent card_id

	// agentMessageCard tracks the currently-open message card for each
	// sub-agent (keyed by agent_id == card_id from EmitSubagentStart).
	// Without this, sub-agent LLM text events (EngineEventText etc.) all
	// land on the main session's messageCardID and the user sees L2/L3
	// turn-of-mind text bleed into emma's message stream, defeating the
	// nested-card hierarchy that EmitSubagentStart sets up.
	agentMessageCard map[string]string
	plans            map[string]string // plan_id → plan card_id
	steps            map[string]string // step_id → step card_id
	pendingPerm      map[string]string // request_id → ⟨request_id⟩ (for prompt.reply correlation)

	// askQuestion maps a v2.2 prompt.user request_id back to the
	// originating engine tool_use_id. ask_user_question is a client-routed
	// tool: the engine's tool executor blocks on a tool.result. v2.2
	// surfaces it as prompt.user(kind=question) instead of card.add(tool);
	// when the user replies with prompt.user_response, conn.go uses this
	// map to find the tool_use_id and dispatch a tool.result that
	// unblocks the engine.
	askQuestion map[string]string // prompt request_id → tool_use_id

	// pendingPlan maps a v2.2 prompt.user request_id to the engine's
	// PlanCoordinator plan_id. Same pattern as askQuestion: the engine
	// blocks on SubmitPlanResponse keyed by plan_id, but on the wire
	// we use a v2.2 request_id. coordinator/conn uses this map to bridge
	// back — without it, the user's prompt.user_response carries a
	// synthetic request_id the engine doesn't recognise and PlanCoordinator
	// hangs forever.
	pendingPlan map[string]string // prompt request_id → engine plan_id

	// pendingStepDecision: wire prompt request_id → engine
	// StepDecisionRequest.RequestID. Same dual-id pattern as
	// pendingPlan / pendingPerm: the engine blocks on its own id while
	// the wire frame is the request_id.
	pendingStepDecision map[string]string

	// pausedCards holds the orphan-watchdog suspension list for each
	// outstanding prompt.user request. While the user is reviewing
	// (plan_review, permission, ask_user_question), the surrounding
	// agent / message / turn cards are intentionally idle and must not
	// fire orphan_timeout. We pause their watchdogs on emit and resume
	// on response — design intent: prompt.user has no time limit.
	pausedCards map[string][]string // prompt request_id → list of paused card_ids

	// toolsFromPlanning tracks which tool cards in `tools` were opened
	// by EngineEventToolPlanning (stream-time early-open) and have not
	// yet been upgraded to Phase=executing by EngineEventToolStart.
	// These are the cards eligible for cancellation on Retract.
	toolsFromPlanning map[string]bool

	// toolNames is a ToolUseID → ToolName cache. ToolPlanning fills
	// it; Progress / Queued / Permission lookups read it back so they
	// don't need the front-end to re-send the name on every state
	// change.
	toolNames map[string]string

	// sessionID is captured on first Translate so picker.Forget can be
	// wired through the channel layer's session lifecycle.
	sessionID string
}

func newSessionState() *sessionState {
	return &sessionState{
		tools:               make(map[string]string),
		subagents:           make(map[string]*emitv2.Emitter),
		subAgentCard:        make(map[string]string),
		agentMessageCard:    make(map[string]string),
		plans:               make(map[string]string),
		steps:               make(map[string]string),
		pendingPerm:         make(map[string]string),
		askQuestion:         make(map[string]string),
		pendingPlan:         make(map[string]string),
		pendingStepDecision: make(map[string]string),
		pausedCards:         make(map[string][]string),
		toolsFromPlanning:   make(map[string]bool),
		toolNames:           make(map[string]string),
	}
}

// scopeFor resolves the Emitter + current message-card-id for events
// attributed to agentID. Sub-agent scope is keyed by agentID == card_id
// of the EmitSubagentStart card; main scope is the session emitter
// itself (agentID empty / "main" / unknown sub-agent).
//
// getMsgCardID / setMsgCardID are paired: getMsgCardID returns "" when
// no message card is open for this scope; setMsgCardID stores the new
// card id under the same key. Callers must hold s.mu.
func (s *sessionState) scopeFor(agentID string, mainEm *emitv2.Emitter) (
	em *emitv2.Emitter,
	getMsgCardID func() string,
	setMsgCardID func(string),
) {
	if agentID != "" && agentID != "main" {
		if subEm, ok := s.subagents[agentID]; ok && subEm != nil {
			return subEm,
				func() string { return s.agentMessageCard[agentID] },
				func(id string) {
					if id == "" {
						delete(s.agentMessageCard, agentID)
					} else {
						s.agentMessageCard[agentID] = id
					}
				}
		}
	}
	return mainEm,
		func() string { return s.messageCardID },
		func(id string) { s.messageCardID = id }
}

// askUserQuestionToolName mirrors internal/tool/askuserquestion.ToolName.
// Hardcoded here to avoid an import cycle.
const askUserQuestionToolName = "ask_user_question"

// suspendForPrompt pauses the orphan watchdog up the chain rooted at the
// most specific tracked card we know for the agent that triggered the
// prompt, falling back to the active message / turn cards. Records the
// paused set under reqID so resumeForPrompt can undo it when the user
// responds. Caller must hold s.mu.
func (t *Translator) suspendForPrompt(s *sessionState, em *emitv2.Emitter, agentID, reqID string) {
	if em == nil || reqID == "" {
		return
	}
	anchor := ""
	if agentID != "" {
		anchor = s.subAgentCard[agentID]
	}
	if anchor == "" {
		anchor = s.messageCardID
	}
	if anchor == "" {
		anchor = s.turnCardID
	}
	if anchor == "" {
		return
	}
	paused := em.SuspendChainFromCard(anchor)
	if len(paused) > 0 {
		s.pausedCards[reqID] = paused
	}
}

// resumeForPrompt reverses suspendForPrompt. Looks up the paused set,
// resumes each card's watchdog, and clears the entry. Safe to call on
// unknown reqIDs (no-op). Resume is intentionally driven by the
// translator (not by the engine response handler) so that any prompt
// flow that goes through emit-then-Resolve* is automatically covered.
// Caller must hold s.mu.
func (t *Translator) resumeForPrompt(s *sessionState, em *emitv2.Emitter, reqID string) {
	if em == nil || reqID == "" {
		return
	}
	paused, ok := s.pausedCards[reqID]
	if !ok {
		return
	}
	delete(s.pausedCards, reqID)
	em.ResumeChain(paused)
}

func (t *Translator) get(sessionID string) *sessionState {
	t.mu.RLock()
	s, ok := t.sessions[sessionID]
	t.mu.RUnlock()
	if ok {
		return s
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok = t.sessions[sessionID]; ok {
		return s
	}
	s = newSessionState()
	s.sessionID = sessionID
	t.sessions[sessionID] = s
	return s
}

// Drop releases per-session translation state. Call when a session
// closes so memory doesn't grow unbounded on long-lived servers.
func (t *Translator) Drop(sessionID string) {
	t.mu.Lock()
	delete(t.sessions, sessionID)
	t.mu.Unlock()
	if t.phrasePicker != nil {
		t.phrasePicker.Forget(sessionID)
	}
}

// Translate converts one EngineEvent into Builder calls on em (the
// session's main Emitter). For sub-agent inner events, the translator
// looks up the child Emitter and routes onto it instead.
//
// Returns no error — translation is best-effort. Unknown events are
// silently ignored (with a registry-side counter via WSSink.UnknownCount
// when the underlying Sink is a WSSink).
func (t *Translator) Translate(em *emitv2.Emitter, sessionID string, ev *types.EngineEvent) {
	if ev == nil || em == nil {
		return
	}
	s := t.get(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emitter == nil {
		s.emitter = em
	}

	switch ev.Type {
	// ----- Message lifecycle -----
	// Each case below routes via scopeFor(ev.AgentID) so sub-agent LLM
	// text streams onto the sub-agent's own message card (parented to
	// the agent card) instead of bleeding into the main session
	// message and losing the nested-hierarchy visualisation in the UI.
	case types.EngineEventMessageStart:
		t.openTurnIfNeeded(s, em)
		scopeEm, getMsg, setMsg := s.scopeFor(ev.AgentID, em)
		if cur := getMsg(); cur != "" {
			// M4 已经预开了，只补 model 字段
			scopeEm.Card(emitv2.CardMessage, cur).Set(map[string]any{
				"model": ev.Model,
			})
			return
		}
		mid := nonEmpty(ev.MessageID, "msg_"+emitv2.NewCardID(emitv2.CardMessage))
		setMsg(mid)
		parent := s.turnCardID
		if ev.AgentID != "" && ev.AgentID != "main" {
			if agentCard := s.subAgentCard[ev.AgentID]; agentCard != "" {
				parent = agentCard
			}
		}
		scopeEm.Card(emitv2.CardMessage, mid).Add(emitv2.MessagePayload{
			Role:  "assistant",
			Model: ev.Model,
		}, emitv2.WithParent(parent))

	case types.EngineEventText:
		t.openTurnIfNeeded(s, em)
		scopeEm, getMsg, _ := t.openAgentMessageIfNeeded(s, em, ev.AgentID, "")
		scopeEm.Card(emitv2.CardMessage, getMsg()).Append(emitv2.ChannelText, ev.Text)

	case types.EngineEventToolUse:
		// LLM signalled it wants to call a tool. The tool input streams
		// as ChannelToolInput on the same message; the actual tool card
		// opens later on EngineEventToolStart. No-op here — the input
		// already arrived through message stream consumers upstream.

	case types.EngineEventMessageDelta:
		// Carries stop_reason + usage. We attach via Set on the open message.
		scopeEm, getMsg, _ := s.scopeFor(ev.AgentID, em)
		cur := getMsg()
		if cur == "" {
			return
		}
		scopeEm.Card(emitv2.CardMessage, cur).Set(map[string]any{
			"stop_reason": ev.StopReason,
		})

	case types.EngineEventMessageStop:
		scopeEm, getMsg, setMsg := s.scopeFor(ev.AgentID, em)
		cur := getMsg()
		if cur == "" {
			return
		}
		var metrics *emitv2.Metrics
		if ev.Usage != nil {
			metrics = &emitv2.Metrics{
				TokensIn:   ev.Usage.InputTokens,
				TokensOut:  ev.Usage.OutputTokens,
				CacheRead:  ev.Usage.CacheRead,
				CacheWrite: ev.Usage.CacheWrite,
			}
		}
		opts := []emitv2.EmitOpt{}
		if metrics != nil {
			opts = append(opts, emitv2.WithMetrics(*metrics))
		}
		scopeEm.Card(emitv2.CardMessage, cur).Close(emitv2.StatusOK, opts...)
		setMsg("")

	// ----- System notices -----
	case types.EngineEventSystemNotice:
		if ev.SystemNotice == nil {
			return
		}
		sn := ev.SystemNotice
		severity := emitv2.SeverityInfo
		if sn.Icon == "warning" {
			severity = emitv2.SeverityWarn
		}
		hint := emitv2.Hint{Title: sn.Title, Summary: sn.Summary}
		if sn.Icon != "" {
			hint.Icon = sn.Icon
		}
		cardID := emitv2.NewCardID(emitv2.CardSystem)
		em.Card(emitv2.CardSystem, cardID).Add(
			emitv2.SystemPayload{
				Topic:      sn.Topic,
				Summary:    sn.Summary,
				ActionHint: sn.ActionHint,
			},
			emitv2.WithHint(hint),
			emitv2.WithSeverity(severity),
		)

	// ----- Tool lifecycle -----
	case types.EngineEventToolPlanning:
		// LLM 流式期间拿到 tool_use block 的 name。
		// 只更新内部跟踪状态，不向客户端发送任何事件。
		// 实际的 card.add 推迟到 EngineEventToolQueued（在 buffer-then-replay
		// 的文字事件之后），确保文字先于工具卡出现在 wire 上。
		// ask_user_question 经由 EngineEventToolCall 升级为 prompt.user，不需要工具卡。
		t.openTurnIfNeeded(s, em)
		if _, exists := s.tools[ev.ToolUseID]; exists {
			return // 幂等
		}
		toolCardID := nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[ev.ToolUseID] = toolCardID
		s.toolsFromPlanning[ev.ToolUseID] = true
		s.toolNames[ev.ToolUseID] = ev.ToolName

	case types.EngineEventToolPlanningProgress:
		// 流式期间工具参数累积。card 在 ToolQueued 才开，此时无需更新客户端。
		if s.toolsFromPlanning[ev.ToolUseID] {
			return
		}
		// 非 planning 路径（罕见）— card 已开，正常 Set。
		toolCardID, ok := s.tools[ev.ToolUseID]
		if !ok {
			return
		}
		toolName := s.toolNames[ev.ToolUseID]
		if toolName == "" {
			toolName = ev.ToolName
		}
		em.Card(emitv2.CardTool, toolCardID).Set(map[string]any{
			"phase":       emitv2.PhasePlanningArgs,
			"phase_hint":  t.pickPhrase(s, toolName, emitv2.PhasePlanningArgs, ev.Bytes, nil),
			"phase_bytes": ev.Bytes,
		})

	case types.EngineEventToolQueued:
		// buffer-then-replay 的文字事件已经发出，现在安全地开工具卡。
		// ask_user_question 走 prompt.user 路径，不需要工具卡。
		toolName := s.toolNames[ev.ToolUseID]
		if toolName == "" {
			toolName = ev.ToolName
		}
		if toolName == askUserQuestionToolName {
			return
		}
		toolCardID, ok := s.tools[ev.ToolUseID]
		if !ok {
			// ToolPlanning 未触发（极少见）— 直接新建 ID
			toolCardID = emitv2.NewCardID(emitv2.CardTool)
			s.tools[ev.ToolUseID] = toolCardID
			s.toolNames[ev.ToolUseID] = toolName
		}
		opts := []emitv2.EmitOpt{
			emitv2.WithParent(parentForTool(s)),
			emitv2.WithoutLifecycle(),
		}
		em.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:      toolName,
			Target:    "server",
			Phase:     emitv2.PhaseQueued,
			PhaseHint: t.pickPhrase(s, toolName, emitv2.PhaseQueued, 0, nil),
		}, opts...)

	case types.EngineEventToolPlanningRetract:
		// callLLM.onRetry 触发。planning 阶段的工具卡从未 card.add 到客户端
		// （推迟到 ToolQueued），只需清理内部跟踪状态。
		for id := range s.toolsFromPlanning {
			delete(s.tools, id)
			delete(s.toolNames, id)
		}
		s.toolsFromPlanning = map[string]bool{}

	case types.EngineEventNextRoundThinking:
		if s.turnCardID == "" {
			return // 没有 turn — 无父，跳过
		}
		if s.messageCardID != "" {
			return // 当前还有未关闭的 message card — 不重复开
		}
		// 提前开新 message card，挂 hint summary
		mid := "msg_" + emitv2.NewCardID(emitv2.CardMessage)
		s.messageCardID = mid
		em.Card(emitv2.CardMessage, mid).Add(emitv2.MessagePayload{Role: "assistant"},
			emitv2.WithParent(s.turnCardID),
			emitv2.WithHint(emitv2.Hint{
				Summary: t.pickPhrase(s, "", emitv2.PhaseNextRound, 0, nil),
			}))

	case types.EngineEventToolStart:
		t.openTurnIfNeeded(s, em)
		input := parseJSONObject(ev.ToolInput)
		scopeEm, getMsg, _ := s.scopeFor(ev.AgentID, em)
		if existing, ok := s.tools[ev.ToolUseID]; ok {
			// ToolQueued 已开卡 — 升级到 executing 而非重开
			scopeEm.Card(emitv2.CardTool, existing).Set(map[string]any{
				"phase":      emitv2.PhaseExecuting,
				"phase_hint": t.pickPhrase(s, ev.ToolName, emitv2.PhaseExecuting, 0, nil),
				"input":      input,
			})
			// 摘除 planning 标记 — 这卡已转正，retract 不应再操作它
			delete(s.toolsFromPlanning, ev.ToolUseID)
			if _, ok := s.toolNames[ev.ToolUseID]; !ok {
				s.toolNames[ev.ToolUseID] = ev.ToolName
			}
			// WithoutLifecycle 设置后无法反取消（emit/v2 已知 limitation）。
			return
		}
		// Fresh open 路径（ToolPlanning / ToolQueued 未触发的兜底）
		toolCardID := nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[ev.ToolUseID] = toolCardID
		s.toolNames[ev.ToolUseID] = ev.ToolName
		opts := []emitv2.EmitOpt{emitv2.WithParent(parentForToolInScope(s, ev.AgentID, getMsg()))}
		// Agent-spawning tools (scheduler / task) wrap entire sub-agent
		// runs that legitimately last tens of minutes. The 120s tool-card
		// orphan_timeout was incorrect for them — once the inner agent
		// stopped tick-ing its own card (e.g. after planner done, while
		// the step DAG runner waits on an L3 to finish), the watchdog would
		// synthesise a failed close on the tool card even though the
		// underlying work was healthy. Opt these out of the watchdog;
		// they still get parent-chain tracking (chain-only mode) so
		// descendant heartbeats refresh the turn above.
		if isOrchestrationTool(ev.ToolName) {
			opts = append(opts, emitv2.WithoutLifecycle())
		}
		scopeEm.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:      ev.ToolName,
			Target:    "server",
			Intent:    ev.Intent,
			Input:     input,
			Phase:     emitv2.PhaseExecuting,
			PhaseHint: t.pickPhrase(s, ev.ToolName, emitv2.PhaseExecuting, 0, nil),
		}, opts...)

	case types.EngineEventToolEnd:
		toolCardID, ok := s.tools[ev.ToolUseID]
		if !ok {
			toolCardID = nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		}
		delete(s.tools, ev.ToolUseID)
		delete(s.toolNames, ev.ToolUseID)
		delete(s.toolsFromPlanning, ev.ToolUseID)
		var inner emitv2.ToolPayload
		var status = emitv2.StatusOK
		var errInfo *emitv2.ErrorInfo
		if ev.ToolResult != nil {
			inner.Output = ev.ToolResult.Content
			if ev.ToolResult.IsError {
				status = emitv2.StatusFailed
				// Trust the tool's structured ErrorType when set;
				// otherwise default to Internal. NewError fills the
				// user_message / retryable defaults from the registry,
				// so the front-end gets a localized fallback even when
				// the tool only said `ErrorType=permission_denied`.
				typ := emitv2.ErrorType(ev.ToolResult.ErrorType)
				if typ == "" {
					typ = emitv2.ErrorTypeInternal
				}
				errInfo = emitv2.NewError(typ, ev.ToolResult.Content)
			}
			// promote known metadata keys to typed fields; everything
			// else flows through to ToolPayload.Metadata verbatim. This
			// is what gives web_search / tavily_search their {urls,
			// query, result_count, has_raw} on the wire — without it
			// the client only sees the formatted text Output.
			inner.RenderHint, inner.Language, inner.FilePath, inner.Metadata =
				promoteToolMetadata(ev.ToolResult.Metadata)
		}
		opts := []emitv2.EmitOpt{emitv2.WithInner(inner)}
		if errInfo != nil {
			opts = append(opts, emitv2.WithError(errInfo))
		}
		// Route close on the same sub-agent Emitter that opened the
		// tool card; without this the close envelope's agent_id is
		// "main" while the open's was the sub-agent's id, and the
		// front-end watchdog can fail to pair them.
		closeEm, _, _ := s.scopeFor(ev.AgentID, em)
		closeEm.Card(emitv2.CardTool, toolCardID).Close(status, opts...)

	case types.EngineEventToolCall:
		// Client-side tool execution. Two paths:
		//   (a) ask_user_question → upgrade to prompt.user(kind=question)
		//       per v2.2 §11. The engine's askUserQuestion tool blocks
		//       on a tool.result, so we record the request_id →
		//       tool_use_id mapping; conn.go converts the user's
		//       prompt.user_response back into a tool.result and
		//       dispatches to engine, which unblocks naturally.
		//   (b) any other client tool → standard tool card.
		t.openTurnIfNeeded(s, em)
		input := parseJSONObject(ev.ToolInput)
		if ev.ToolName == askUserQuestionToolName {
			question, options, multi, allowCustom := decodeAskQuestionInput(input)
			payload := emitv2.QuestionPromptPayload{
				Question:    question,
				Options:     options,
				Multi:       multi,
				AllowCustom: allowCustom,
			}
			reqID := emitv2.NewRequestID()
			if err := t.persistWait(em, reqID, wait.KindQuestion, ev.ToolUseID,
				"prompt.user", "question", payload); err != nil {
				// Persistence failed: do NOT emit the prompt. The user
				// would never get an answerable card. Engine's
				// askUserQuestion tool will eventually time out / get
				// cancelled normally.
				return
			}
			em.PromptUserWithID(reqID, "question", payload)
			s.askQuestion[reqID] = ev.ToolUseID
			// 清理 planning 阶段留下的内部跟踪记录（card 从未发到客户端）。
			delete(s.tools, ev.ToolUseID)
			delete(s.toolNames, ev.ToolUseID)
			delete(s.toolsFromPlanning, ev.ToolUseID)
			t.suspendForPrompt(s, em, ev.AgentID, reqID)
			return
		}
		toolCardID := nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[ev.ToolUseID] = toolCardID
		scopeEm, getMsg, _ := s.scopeFor(ev.AgentID, em)
		opts := []emitv2.EmitOpt{emitv2.WithParent(parentForToolInScope(s, ev.AgentID, getMsg()))}
		// Symmetry with EngineEventToolStart path (line 287): orchestration
		// tools (scheduler / task) wrap multi-minute sub-agent runs that
		// legitimately outlast the CardTool 120s orphan watchdog. Opt them
		// out here too — previously only the ToolStart path had this fix,
		// leaving client-side tool calls (this case) subject to false-
		// positive orphan_timeout closes on the wire card.
		if isOrchestrationTool(ev.ToolName) {
			opts = append(opts, emitv2.WithoutLifecycle())
		}
		scopeEm.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:           ev.ToolName,
			Target:         "client",
			Intent:         ev.Intent,
			AwaitSessionID: ev.AwaitSessionID,
			Input:          input,
		}, opts...)

	case types.EngineEventAgentIntent:
		// Carry per-tool intent as a tick on the tool card if it's open;
		// otherwise emit on the active message card.
		card := s.tools[ev.ToolUseID]
		kind := emitv2.CardTool
		if card == "" {
			card = s.messageCardID
			kind = emitv2.CardMessage
		}
		if card == "" {
			return
		}
		em.Card(kind, card).Tick(emitv2.TickIntent, emitv2.IntentPayload{Intent: ev.Intent})

	// ----- Sub-agent lifecycle -----
	case types.EngineEventSubAgentStart:
		t.openTurnIfNeeded(s, em)
		// Spawn a child Emitter with the sub-agent's identity.
		role := emitv2.RoleWorker
		runID := emitv2.NewAgentRunID()
		child := em.Sub(ev.AgentID, role, runID)
		s.subagents[ev.AgentID] = child
		// Open the agent card on the child (envelope.agent_id auto-bound).
		agentCardID := nonEmpty(ev.AgentID, emitv2.NewCardID(emitv2.CardAgent))
		s.subAgentCard[ev.AgentID] = agentCardID
		parent := parentForSubAgent(s, ev.ParentAgentID, ev.ParentStepID)
		// LoadedSkills passthrough: convert from pkg/types shape to the
		// emitv2 wire shape. Field-for-field copy; we keep them as two
		// types so pkg/types doesn't import emitv2 (one-way dependency).
		var skillsWire []emitv2.LoadedSkillInfo
		if len(ev.LoadedSkills) > 0 {
			skillsWire = make([]emitv2.LoadedSkillInfo, 0, len(ev.LoadedSkills))
			for _, s := range ev.LoadedSkills {
				skillsWire = append(skillsWire, emitv2.LoadedSkillInfo{
					Name:    s.Name,
					Version: s.Version,
					Source:  s.Source,
				})
			}
		}
		// Name drives the CardAgent hint.title template "{name}". When the
		// caller leaves AgentName empty (the scheduler tool deliberately
		// does this to avoid duplicating SubagentType in two places — see
		// internal/tool/scheduler/scheduler.go:171), fall back to
		// SubagentType so the card surfaces "scheduler"/"freelancer"
		// instead of the literal "{name}" placeholder leaking to the UI.
		agentName := ev.AgentName
		if agentName == "" {
			agentName = ev.SubagentType
		}
		child.Card(emitv2.CardAgent, agentCardID).Add(emitv2.AgentPayload{
			Name:          agentName,
			AgentType:     ev.AgentType,
			SubagentType:  ev.SubagentType,
			ParentAgentID: ev.ParentAgentID,
			TaskPrompt:    ev.AgentTask,
			LoadedSkills:  skillsWire,
		}, emitv2.WithParent(parent))

	case types.EngineEventSubAgentEvent:
		t.translateSubAgentEvent(s, ev)

	case types.EngineEventSubAgentEnd:
		child, ok := s.subagents[ev.AgentID]
		agentCardID := s.subAgentCard[ev.AgentID]
		delete(s.subagents, ev.AgentID)
		delete(s.subAgentCard, ev.AgentID)
		if !ok || agentCardID == "" {
			return
		}
		var metrics *emitv2.Metrics
		if ev.Usage != nil || ev.Duration > 0 {
			metrics = &emitv2.Metrics{
				DurationMs: ev.Duration,
			}
			if ev.Usage != nil {
				metrics.TokensIn = ev.Usage.InputTokens
				metrics.TokensOut = ev.Usage.OutputTokens
				metrics.CacheRead = ev.Usage.CacheRead
				metrics.CacheWrite = ev.Usage.CacheWrite
			}
		}
		var refs []emitv2.ArtifactRef
		for _, a := range ev.Artifacts {
			refs = append(refs, artifactRefFromV1(a))
		}
		opts := []emitv2.EmitOpt{
			emitv2.WithInner(emitv2.AgentPayload{
				// SubagentType repeated here so the close payload also
				// carries the worker label — front-ends that read close
				// frames in isolation (e.g. metrics dashboards aggregating
				// finished agents) don't need to remember what add said.
				SubagentType: ev.SubagentType,
				NumTurns:     safeTurnCount(ev),
				DeniedTools:  ev.DeniedTools,
				Artifacts:    refs,
			}),
		}
		if metrics != nil {
			opts = append(opts, emitv2.WithMetrics(*metrics))
		}
		status := emitv2.StatusOK
		if ev.AgentStatus == "error" || ev.AgentStatus == "failed" {
			status = emitv2.StatusFailed
			// Attach an ErrorInfo built from Terminal so the client can
			// render WHY the sub-agent failed. Previously close payload
			// only carried {subagent_type, num_turns} and the UI
			// rendered an opaque "failed" badge — operators had to grep
			// service.log to find the underlying llm.call / contract
			// error. The 2026-06-02 freelancer 400 was the canonical
			// case: card.close.payload had no error field at all.
			if errInfo := errorInfoFromTerminal(ev.Terminal); errInfo != nil {
				opts = append(opts, emitv2.WithError(errInfo))
			}
		}
		child.Card(emitv2.CardAgent, agentCardID).Close(status, opts...)

	// ----- Plan lifecycle -----
	case types.EngineEventPlanCreated:
		t.openPlan(s, em, ev, false)
	case types.EngineEventPlanUpdated:
		if pe := ev.PlanEvent; pe != nil {
			cardID := s.plans[pe.PlanID]
			if cardID == "" {
				t.openPlan(s, em, ev, false)
				return
			}
			em.Card(emitv2.CardPlan, cardID).Set(plansFromTasks(pe))
		}
	case types.EngineEventPlanCompleted, types.EngineEventPlanFailed:
		if pe := ev.PlanEvent; pe != nil {
			cardID := s.plans[pe.PlanID]
			if cardID == "" {
				return
			}
			delete(s.plans, pe.PlanID)
			status := emitv2.StatusOK
			var errInfo *emitv2.ErrorInfo
			if ev.Type == types.EngineEventPlanFailed {
				status = emitv2.StatusFailed
				errInfo = errorFromTaskDispatch(ev.TaskDispatch, ev)
			}
			opts := []emitv2.EmitOpt{}
			if errInfo != nil {
				opts = append(opts, emitv2.WithError(errInfo))
			}
			em.Card(emitv2.CardPlan, cardID).Close(status, opts...)
		}

	// ----- Step lifecycle -----
	case types.EngineEventStepDispatched:
		t.openStep(s, em, ev)
	case types.EngineEventStepStarted:
		td := ev.TaskDispatch
		if td == nil {
			return
		}
		cardID := s.steps[td.TaskID]
		if cardID == "" {
			return
		}
		em.Card(emitv2.CardStep, cardID).Set(emitv2.StepPayload{Status: "running"})
	case types.EngineEventStepProgress:
		td := ev.TaskDispatch
		if td == nil {
			return
		}
		cardID := s.steps[td.TaskID]
		if cardID == "" {
			return
		}
		em.Card(emitv2.CardStep, cardID).Tick(emitv2.TickProgress, emitv2.ProgressPayload{})
	case types.EngineEventStepCompleted:
		t.closeStep(s, em, ev, emitv2.StatusOK)
	case types.EngineEventStepFailed:
		t.closeStep(s, em, ev, emitv2.StatusFailed)
	case types.EngineEventStepSkipped:
		t.closeStep(s, em, ev, emitv2.StatusSkipped)

	// ----- Permission / Plan review prompts -----
	case types.EngineEventPermissionRequest:
		if ev.PermissionRequest == nil {
			return
		}
		opts := make([]emitv2.PermissionOption, 0, len(ev.PermissionRequest.Options))
		for _, o := range ev.PermissionRequest.Options {
			opts = append(opts, emitv2.PermissionOption{
				Label: o.Label,
				Scope: string(o.Scope),
				Allow: o.Allow,
			})
		}
		payload := emitv2.PermissionPromptPayload{
			ToolName:      ev.PermissionRequest.ToolName,
			ToolInput:     ev.PermissionRequest.ToolInput,
			Message:       ev.PermissionRequest.Message,
			IsReadOnly:    ev.PermissionRequest.IsReadOnly,
			Options:       opts,
			PermissionKey: ev.PermissionRequest.PermissionKey,
		}
		reqID := emitv2.NewRequestID()
		if err := t.persistWait(em, reqID, wait.KindPermission,
			ev.PermissionRequest.RequestID, "prompt.user", "permission", payload); err != nil {
			return
		}
		em.PromptUserWithID(reqID, "permission", payload)
		// Engine's pending-permissions map is keyed on the engine-side
		// PermissionRequest.RequestID (e.g. "perm_a1b2c3d4"); the wire
		// request_id is independent ("req_..."). Map them so conn.go
		// can build a PermissionResponse with the engine ID.
		s.pendingPerm[reqID] = ev.PermissionRequest.RequestID
		t.suspendForPrompt(s, em, ev.AgentID, reqID)

		// Phase=permission_wait on the tool card if open. This complements
		// the prompt.user(permission) frame above — the card visually
		// transitions from "executing" to "waiting for authorization".
		if reqInfo := ev.PermissionRequest; reqInfo != nil && reqInfo.ToolUseID != "" {
			if toolCardID, ok := s.tools[reqInfo.ToolUseID]; ok {
				toolName := s.toolNames[reqInfo.ToolUseID]
				if toolName == "" {
					toolName = reqInfo.ToolName
				}
				em.Card(emitv2.CardTool, toolCardID).Set(map[string]any{
					"phase":      emitv2.PhasePermissionWait,
					"phase_hint": t.pickPhrase(s, toolName, emitv2.PhasePermissionWait, 0, nil),
				})
			}
		}

	case types.EngineEventPlanProposed:
		if ev.PlanProposal == nil {
			return
		}
		steps := make([]emitv2.PlanReviewStep, 0, len(ev.PlanProposal.Steps))
		for _, st := range ev.PlanProposal.Steps {
			steps = append(steps, emitv2.PlanReviewStep{
				ID:           st.ID,
				SubagentType: st.SubagentType,
				Description:  st.Description,
				Prompt:       st.Prompt,
				DependsOn:    st.DependsOn,
			})
		}
		payload := emitv2.PlanReviewPromptPayload{
			PlanID:             ev.PlanProposal.PlanID,
			Goal:               ev.PlanProposal.Goal,
			Rationale:          ev.PlanProposal.Rationale,
			Steps:              steps,
			AvailableSubagents: ev.PlanProposal.AvailableSubagents,
		}
		reqID := emitv2.NewRequestID()
		if err := t.persistWait(em, reqID, wait.KindPlanReview,
			ev.PlanProposal.PlanID, "prompt.user", "plan_review", payload); err != nil {
			return
		}
		em.PromptUserWithID(reqID, "plan_review", payload, emitv2.WithoutLifecycle())
		// Remember the mapping so the user's prompt.user_response can
		// be routed back to the engine's plan_id-keyed PlanCoordinator.
		s.pendingPlan[reqID] = ev.PlanProposal.PlanID
		t.suspendForPrompt(s, em, ev.AgentID, reqID)

	case types.EngineEventPlanApproved:
		if ev.PlanProposal != nil {
			em.PromptReply("", "approved", "")
		}

	case types.EngineEventStepDecisionRequested:
		sd := ev.StepDecision
		if sd == nil {
			return
		}
		payload := emitv2.StepDecisionPromptPayload{
			Scope:           sd.Scope,
			StepID:          sd.StepID,
			StepDescription: sd.StepDescription,
			Reason:          sd.Reason,
			Attempts:        sd.Attempts,
			AllowRetry:      sd.AllowRetry,
		}
		reqID := emitv2.NewRequestID()
		if err := t.persistWait(em, reqID, wait.KindStepDecision,
			sd.RequestID, "prompt.user", "step_decision", payload); err != nil {
			return
		}
		em.PromptUserWithID(reqID, "step_decision", payload, emitv2.WithoutLifecycle())
		// Map wire reqID → engine-side StepDecisionRequest.RequestID so
		// conn.go's prompt.user_response can synthesise a typed
		// StepDecisionResponse keyed on the engine identifier.
		s.pendingStepDecision[reqID] = sd.RequestID
		// Same suspend policy as plan_review / question / permission:
		// the user is the gating actor, so the surrounding agent / step
		// / message / turn cards must not orphan-timeout while waiting.
		t.suspendForPrompt(s, em, ev.AgentID, reqID)

	case types.EngineEventLLMHeartbeat:
		// Pick the most-specific open card to tick: sub-agent's
		// CardAgent (which sits under the step card in plan mode →
		// heartbeat walks up step → plan → turn) when available,
		// else the active message card (L1 main loop). Drop silently
		// if neither exists — would only happen pre-turn-open, which
		// is fine because there's no parent chain to keep alive.
		var (
			cardID string
			kind   emitv2.CardKind
			em2    = em
		)
		if ev.AgentID != "" {
			if id := s.subAgentCard[ev.AgentID]; id != "" {
				cardID, kind = id, emitv2.CardAgent
				if child := s.subagents[ev.AgentID]; child != nil {
					em2 = child
				}
			}
		}
		if cardID == "" && s.messageCardID != "" {
			cardID, kind = s.messageCardID, emitv2.CardMessage
		}
		if cardID == "" {
			return
		}
		em2.Card(kind, cardID).Tick(emitv2.TickHeartbeat, emitv2.HeartbeatPayload{
			UptimeMs: ev.Duration,
		})

	case types.EngineEventTextReset:
		// callLLM retry after attempt 1 streamed partial text live.
		// Close the in-progress message card so the stale prefix is
		// discarded; the next EngineEventText chunk opens a fresh card.
		// Route by agent so a sub-agent retry doesn't accidentally
		// reset the main session's message card.
		scopeEm, getMsg, setMsg := s.scopeFor(ev.AgentID, em)
		cur := getMsg()
		if cur == "" {
			return
		}
		scopeEm.Card(emitv2.CardMessage, cur).Close(emitv2.StatusOK)
		setMsg("")

	case types.EngineEventLLMRetry:
		// Surface retry status to the wire. Same card-routing logic as
		// the heartbeat case (sub-agent CardAgent if known, else the
		// active L1 message card); without this the front-end has no
		// signal that the server is in a backoff loop — looks identical
		// to a slow upstream. Renders as card.tick(kind=note) with a
		// human-readable summary so the existing v2.2 note rendering
		// path handles it without a wire-protocol bump.
		if ev.LLMRetry == nil {
			return
		}
		var (
			cardID string
			kind   emitv2.CardKind
			em2    = em
		)
		if ev.AgentID != "" {
			if id := s.subAgentCard[ev.AgentID]; id != "" {
				cardID, kind = id, emitv2.CardAgent
				if child := s.subagents[ev.AgentID]; child != nil {
					em2 = child
				}
			}
		}
		if cardID == "" && s.messageCardID != "" {
			cardID, kind = s.messageCardID, emitv2.CardMessage
		}
		if cardID == "" {
			return
		}
		em2.Card(kind, cardID).Tick(emitv2.TickNote, emitv2.NotePayload{
			Text:     formatRetryNote(ev.LLMRetry),
			Severity: emitv2.SeverityWarn,
		})

	// ----- Done / Error -----
	case types.EngineEventDone:
		// Close any open message and the turn.
		if s.messageCardID != "" {
			em.Card(emitv2.CardMessage, s.messageCardID).Close(emitv2.StatusOK)
			s.messageCardID = ""
		}
		if s.turnCardID != "" {
			var metrics emitv2.Metrics
			if ev.Usage != nil {
				metrics.TokensIn = ev.Usage.InputTokens
				metrics.TokensOut = ev.Usage.OutputTokens
			}
			em.Card(emitv2.CardTurn, s.turnCardID).Close(emitv2.StatusOK, emitv2.WithMetrics(metrics))
			s.turnCardID = ""
		}
		// Reset all per-turn state.
		s.tools = make(map[string]string)
		s.plans = make(map[string]string)
		s.steps = make(map[string]string)

	case types.EngineEventError:
		errInfo := buildEngineErrorInfo(ev)
		em.SessionEvent("error", map[string]any{"error": errInfo})

	default:
		// Unknown event types are silently dropped — registry tests
		// guard the v2.2-side enum, and the engine v1 enum is fixed.
		// New v1 event types added in the future without translator
		// updates simply don't appear on the wire (a fail-safe vs
		// crashing or sending malformed v2 frames).
	}
}

// ----- helpers -----

func (t *Translator) openTurnIfNeeded(s *sessionState, em *emitv2.Emitter) {
	if s.turnCardID != "" {
		return
	}
	s.turnNo++
	s.turnCardID = emitv2.NewCardID(emitv2.CardTurn)
	em.Card(emitv2.CardTurn, s.turnCardID).Add(emitv2.TurnPayload{
		TurnNo:  s.turnNo,
		Channel: "chat",
	})
}

func (t *Translator) openMessageIfNeeded(s *sessionState, em *emitv2.Emitter, model string) {
	if s.messageCardID != "" {
		return
	}
	s.messageCardID = emitv2.NewCardID(emitv2.CardMessage)
	em.Card(emitv2.CardMessage, s.messageCardID).Add(emitv2.MessagePayload{
		Role:  "assistant",
		Model: model,
	}, emitv2.WithParent(s.turnCardID))
}

// openAgentMessageIfNeeded mirrors openMessageIfNeeded for a specific
// sub-agent scope: opens a fresh message card on the sub-agent's
// Emitter (parented to the agent card so the wire envelope's
// parent_card_id nests under the agent), and records it in
// agentMessageCard. Falls back to the main-scope helper when agentID
// resolves to the main emitter.
func (t *Translator) openAgentMessageIfNeeded(s *sessionState, mainEm *emitv2.Emitter, agentID, model string) (em *emitv2.Emitter, getMsg func() string, setMsg func(string)) {
	em, getMsg, setMsg = s.scopeFor(agentID, mainEm)
	if getMsg() != "" {
		return em, getMsg, setMsg
	}
	if em == mainEm {
		// Main scope still needs a turn anchor.
		t.openMessageIfNeeded(s, em, model)
		return em, getMsg, setMsg
	}
	// Sub-agent scope: parent the message card to the sub-agent's
	// agent card so the wire envelope's parent_card_id nests under L2/L3.
	parentCard := s.subAgentCard[agentID]
	mid := emitv2.NewCardID(emitv2.CardMessage)
	em.Card(emitv2.CardMessage, mid).Add(emitv2.MessagePayload{
		Role:  "assistant",
		Model: model,
	}, emitv2.WithParent(parentCard))
	setMsg(mid)
	return em, getMsg, setMsg
}

// parentForToolInScope returns the right tool parent card for the
// agent whose scope is provided. Prefers the sub-agent's own current
// message card (so the tool nests under the sub-agent's "回复" card),
// falls back to the sub-agent card itself, then to the main scope
// chain (legacy behavior).
func parentForToolInScope(s *sessionState, agentID, scopeMsgCardID string) string {
	if scopeMsgCardID != "" {
		return scopeMsgCardID
	}
	if agentID != "" && agentID != "main" {
		if id := s.subAgentCard[agentID]; id != "" {
			return id
		}
	}
	return parentForTool(s)
}

// parentForTool decides what card a tool attaches to. Prefer the most
// recent open message (causal — tool was requested in a message),
// falling back to the turn.
func parentForTool(s *sessionState) string {
	if s.messageCardID != "" {
		return s.messageCardID
	}
	return s.turnCardID
}

// parentForSubAgent decides what card a sub-agent attaches to. Plan /
// orchestrate dispatches carry parentStepID so the agent card can be
// rooted under the step card. Direct tool-dispatch spawns reuse the same
// field for the parent tool_use_id so parallel tool calls do not race into
// an arbitrary open tool card. Non-explicit dispatches fall back to the
// legacy "parent agent → tool → message → turn" chain.
func parentForSubAgent(s *sessionState, parentAgentID, parentStepID string) string {
	if parentStepID != "" {
		if id := s.steps[parentStepID]; id != "" {
			return id
		}
		if id := s.tools[parentStepID]; id != "" {
			return id
		}
	}
	// If the parent agent has a card open, attach there.
	if id := s.subAgentCard[parentAgentID]; id != "" {
		return id
	}
	// Else attach to the most recent tool card (task / scheduler call).
	for _, id := range s.tools {
		return id
	}
	if s.messageCardID != "" {
		return s.messageCardID
	}
	return s.turnCardID
}

// translateSubAgentEvent forwards an inner sub-agent event onto its
// dedicated child Emitter.
func (t *Translator) translateSubAgentEvent(s *sessionState, ev *types.EngineEvent) {
	child, ok := s.subagents[ev.AgentID]
	if !ok || ev.SubAgentEvent == nil {
		return
	}
	inner := ev.SubAgentEvent
	switch inner.EventType {
	case "tool_start":
		toolCardID := nonEmpty(inner.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[inner.ToolUseID] = toolCardID
		input := parseJSONObject(inner.ToolInput)
		opts := []emitv2.EmitOpt{emitv2.WithParent(s.subAgentCard[ev.AgentID])}
		// Symmetry with EngineEventToolStart (line 312) and EngineEventToolCall
		// (line 402): orchestration tools (scheduler / task) wrap multi-minute
		// sub-agent runs that legitimately outlast the CardTool 120s orphan
		// watchdog. Without this, when a sub-agent (e.g. scheduler) dispatches
		// task, the inner tool_start arrives wrapped in SubAgentEvent and hits
		// this path — previously skipping the opt-out, so the task card got
		// killed at 120s while its inner plan-coord was still running for 8 min,
		// surfacing as "执行超时了，我得放弃这步" mid-task.
		if isOrchestrationTool(inner.ToolName) {
			opts = append(opts, emitv2.WithoutLifecycle())
		}
		child.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:   inner.ToolName,
			Target: "server",
			Input:  input,
		}, opts...)
	case "tool_end":
		toolCardID, ok := s.tools[inner.ToolUseID]
		if !ok {
			toolCardID = nonEmpty(inner.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		}
		delete(s.tools, inner.ToolUseID)
		var refs []emitv2.ArtifactRef
		for _, a := range inner.Artifacts {
			refs = append(refs, artifactRefFromV1(a))
		}
		status := emitv2.StatusOK
		if inner.IsError {
			status = emitv2.StatusFailed
		}
		child.Card(emitv2.CardTool, toolCardID).Close(status,
			emitv2.WithInner(emitv2.ToolPayload{
				Output:    inner.Output,
				Artifacts: refs,
			}),
		)
	case "intent":
		card := s.tools[inner.ToolUseID]
		if card == "" {
			return
		}
		child.Card(emitv2.CardTool, card).Tick(emitv2.TickIntent, emitv2.IntentPayload{Intent: inner.Intent})
	}
}

func (t *Translator) openPlan(s *sessionState, em *emitv2.Emitter, ev *types.EngineEvent, _ bool) {
	pe := ev.PlanEvent
	if pe == nil {
		return
	}
	cardID := nonEmpty(pe.PlanID, emitv2.NewCardID(emitv2.CardPlan))
	s.plans[pe.PlanID] = cardID
	steps := make([]emitv2.PlanStepInfo, 0, len(pe.Tasks))
	for _, st := range pe.Tasks {
		steps = append(steps, emitv2.PlanStepInfo{
			StepID:            st.TaskID,
			SubagentType:      st.SubagentType,
			DependsOn:         st.DependsOn,
			UserFacingTitle:   st.UserFacingTitle,
			UserFacingSummary: st.UserFacingSummary,
		})
	}
	em.Card(emitv2.CardPlan, cardID).Add(emitv2.PlanPayload{
		PlanID:   pe.PlanID,
		Goal:     pe.Goal,
		Strategy: pe.Strategy,
		Steps:    steps,
	}, emitv2.WithParent(parentForPlan(s, ev)))
}

// isOrchestrationTool reports whether name belongs to a tool that
// spawns a sub-agent and therefore wraps a multi-minute lifecycle.
// These tools' cards must not be subject to the CardTool 120s
// orphan_timeout: the inner agent legitimately runs longer than that,
// and the watchdog killing the wrapping tool card surfaces as a
// "工具失败" UI artifact while the underlying step still runs and
// eventually succeeds (the exact mis-report users have been seeing).
//
// Tool names are stable LLM-facing identifiers (declared as ToolName
// constants in internal/tool/scheduler and internal/tool/agenttool).
// New orchestration tools added in the future need a one-line update
// here — the alternative (introspecting a tool registry from inside
// the translator) doesn't fit the wire-translator's scope.
func isOrchestrationTool(name string) bool {
	switch name {
	case "scheduler", "freelance":
		return true
	default:
		return false
	}
}

// parentForPlan picks the right parent card_id for a plan card. The
// plan is created by an L2 coordinator (typically the scheduler
// agent), so it should sit UNDER that agent's card on the wire — not
// beside it under the turn. Wrong parent topology was the root cause of
// the orphan_timeout false-positives on the scheduler tool card:
// the plan was a sibling of the tool card, so L3 heartbeats walked
// L3→step→plan→turn without ever touching tool, and the tool card
// (120s timeout) died as soon as the planner stopped tick-ing it.
//
// Precedence:
//  1. emitting agent's CardAgent (subAgentCard[ev.AgentID]) — the
//     scheduler agent under whose roof this plan was produced
//  2. emitting agent's enclosing tool card — best-effort fallback when
//     for some reason the agent card isn't tracked (transient state
//     between agent_end / next agent_start)
//  3. turn card — legacy behaviour, last resort
//
// With (1), the full chain becomes
//
//	l3_agent → step → plan → scheduler_agent → tool → turn
//
// and any heartbeat anywhere in that subtree refreshes the tool card.
func parentForPlan(s *sessionState, ev *types.EngineEvent) string {
	if ev != nil && ev.AgentID != "" {
		if cardID := s.subAgentCard[ev.AgentID]; cardID != "" {
			return cardID
		}
	}
	return s.turnCardID
}

func plansFromTasks(pe *types.PlanEvent) emitv2.PlanPayload {
	steps := make([]emitv2.PlanStepInfo, 0, len(pe.Tasks))
	for _, st := range pe.Tasks {
		steps = append(steps, emitv2.PlanStepInfo{
			StepID:       st.TaskID,
			SubagentType: st.SubagentType,
			DependsOn:    st.DependsOn,
		})
	}
	return emitv2.PlanPayload{
		PlanID:   pe.PlanID,
		Goal:     pe.Goal,
		Strategy: pe.Strategy,
		Steps:    steps,
	}
}

func (t *Translator) openStep(s *sessionState, em *emitv2.Emitter, ev *types.EngineEvent) {
	td := ev.TaskDispatch
	if td == nil {
		return
	}
	cardID := nonEmpty(td.TaskID, emitv2.NewCardID(emitv2.CardStep))
	s.steps[td.TaskID] = cardID
	em.Card(emitv2.CardStep, cardID).Add(emitv2.StepPayload{
		StepID:       td.TaskID,
		SubagentType: td.SubagentType,
		Status:       "queued",
		InputSummary: td.InputSummary,
	}, emitv2.WithParent(parentForStep(s, td)))
}

func parentForStep(s *sessionState, td *types.TaskDispatch) string {
	// Steps always belong to a plan; if we have one open, use it.
	for _, id := range s.plans {
		return id
	}
	return s.turnCardID
}

func (t *Translator) closeStep(s *sessionState, em *emitv2.Emitter, ev *types.EngineEvent, status emitv2.Status) {
	td := ev.TaskDispatch
	if td == nil {
		return
	}
	cardID := s.steps[td.TaskID]
	if cardID == "" {
		return
	}
	delete(s.steps, td.TaskID)
	opts := []emitv2.EmitOpt{}
	if status == emitv2.StatusFailed {
		opts = append(opts, emitv2.WithError(errorFromTaskDispatch(td, ev)))
	}
	if td.OutputSummary != "" || len(td.Deliverables) > 0 {
		opts = append(opts, emitv2.WithInner(emitv2.StepPayload{
			OutputSummary: td.OutputSummary,
			Attempts:      td.Attempts,
			Deliverables:  td.Deliverables,
		}))
	}
	em.Card(emitv2.CardStep, cardID).Close(status, opts...)
}

func errorFromTaskDispatch(td *types.TaskDispatch, ev *types.EngineEvent) *emitv2.ErrorInfo {
	if td == nil {
		return emitv2.NewError(emitv2.ErrorTypeInternal, errMsg(ev.Error))
	}
	typ := emitv2.ErrorType(td.ErrorType)
	if typ == "" {
		typ = emitv2.ErrorTypeInternal
	}
	msg := td.Error
	if msg == "" {
		msg = errMsg(ev.Error)
	}
	e := emitv2.NewError(typ, msg)
	if td.UserMessage != "" {
		e = e.WithUserMessage(td.UserMessage)
	}
	if td.ErrorCode != "" {
		e = e.WithCode(td.ErrorCode)
	}
	if td.Retryable {
		e = e.WithRetryable(true)
	}
	return e
}

// buildEngineErrorInfo derives the wire-level ErrorInfo from an
// EngineEventError. The router can attach a Terminal.Reason
// (multimodal Gate uses TerminalUnsupportedModality) plus an
// ErrorDetails map carrying the rich payload. This helper inspects
// both and lifts the typed fields onto ErrorInfo so the client gets
// `error.type` / `error.user_message` / `error.details` in one shot
// instead of always falling back to ErrorTypeInternal.
func buildEngineErrorInfo(ev *types.EngineEvent) *emitv2.ErrorInfo {
	typ := emitv2.ErrorTypeInternal
	if ev.Terminal != nil {
		switch ev.Terminal.Reason {
		case types.TerminalUnsupportedModality:
			typ = emitv2.ErrorTypeUnsupportedModality
		case types.TerminalModelError:
			typ = emitv2.ErrorTypeModelError
		case types.TerminalImageError:
			typ = emitv2.ErrorTypeInvalidInput
		}
	}
	info := emitv2.NewError(typ, errMsg(ev.Error))

	if ev.ErrorDetails != nil {
		if um, ok := ev.ErrorDetails["user_message"].(string); ok && um != "" {
			info = info.WithUserMessage(um)
		}
		if code, ok := ev.ErrorDetails["error_code"].(string); ok && code != "" {
			info = info.WithCode(code)
		}
		// Surface the remaining structured keys (model /
		// rejected_modalities / etc.) as opaque details. The keys we
		// already lifted above are kept inside Details too so the
		// renderer has a single map to scan.
		details := make(map[string]any, len(ev.ErrorDetails))
		for k, v := range ev.ErrorDetails {
			if k == "user_message" || k == "error_code" {
				continue
			}
			details[k] = v
		}
		info = info.WithDetails(details)
	}
	return info
}

// errorInfoFromTerminal builds a wire ErrorInfo from the sub-agent
// loop's Terminal struct so the front-end can show the actual cause
// of a `subagent_end status=failed` instead of an opaque badge.
//
// Mapping table (Terminal.Reason → emitv2.ErrorType):
//
//	TerminalModelError       → model_error      (upstream LLM API failed)
//	TerminalMaxTurns         → max_turns
//	TerminalAbortedStreaming → user_aborted
//	TerminalAbortedTools     → user_aborted
//	default / TerminalCompleted (failed status)
//	                          → internal        (genuinely unknown — message
//	                                              still carried so operators
//	                                              can grep)
//
// Returns nil when the Terminal pointer is nil — caller decides whether
// to fall back to a generic "internal" badge. The Terminal.Message
// field (e.g. the bifrost 400 string) is forwarded as ErrorInfo.Message
// so it shows up next to the badge in the client.
func errorInfoFromTerminal(term *types.Terminal) *emitv2.ErrorInfo {
	if term == nil {
		return nil
	}
	typ := emitv2.ErrorTypeInternal
	switch term.Reason {
	case types.TerminalModelError:
		typ = emitv2.ErrorTypeModelError
	case types.TerminalMaxTurns:
		typ = emitv2.ErrorTypeMaxTurns
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		typ = emitv2.ErrorTypeUserAborted
	}
	msg := term.Message
	if msg == "" {
		msg = string(term.Reason)
	}
	info := emitv2.NewError(typ, msg)
	// Stash the raw Terminal.Reason so the client can branch on the
	// engine-internal taxonomy too (e.g. distinguish model_error vs
	// user_aborted without a fragile string match on Message).
	info = info.WithDetails(map[string]any{
		"terminal_reason": string(term.Reason),
		"terminal_turn":   term.Turn,
	})
	return info
}

// artifactRefFromV1 converts the v1 ArtifactRef shape into v2.
func artifactRefFromV1(a types.ArtifactRef) emitv2.ArtifactRef {
	return emitv2.ArtifactRef{
		ArtifactID:  a.ArtifactID,
		Name:        a.Name,
		Type:        string(a.Type),
		MimeType:    a.MIMEType,
		SizeBytes:   int(a.SizeBytes),
		Description: a.Description,
		Role:        a.Role,
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func safeTurnCount(ev *types.EngineEvent) int {
	if ev.Terminal != nil {
		return ev.Terminal.Turn
	}
	return 0
}

// promoteToolMetadata extracts well-known keys (render_hint / language /
// file_path) into their typed fields and returns the remainder as
// the passthrough Metadata map. Returned map is nil when nothing
// remains, so the wire frame omits the field rather than carrying an
// empty object.
//
// Why this matters: web_search and tavily_search hang structured data
// (urls / query / result_count / has_raw) off Metadata for the client
// to render as URL chips. Without passthrough the wire would carry
// only the formatted text Output and the client would fall back to
// rendering plain text.
func promoteToolMetadata(meta map[string]any) (renderHint, language, filePath string, rest map[string]any) {
	if len(meta) == 0 {
		return
	}
	rest = make(map[string]any, len(meta))
	for k, v := range meta {
		switch k {
		case "render_hint":
			if s, ok := v.(string); ok {
				renderHint = s
				continue
			}
		case "language":
			if s, ok := v.(string); ok {
				language = s
				continue
			}
		case "file_path":
			if s, ok := v.(string); ok {
				filePath = s
				continue
			}
		}
		rest[k] = v
	}
	if len(rest) == 0 {
		rest = nil
	}
	return
}

// parseJSONObject best-effort parses a JSON object string into map[string]any.
// Used to convert tool input JSON in EngineEvent.ToolInput into the
// structured map form expected by emitv2.ToolPayload.Input. Returns nil
// on parse failure (the protocol allows missing input).
func parseJSONObject(s string) map[string]any {
	if s == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// SinceMs returns the elapsed milliseconds between t and now. Used by
// translation paths that need to fabricate Metrics.DurationMs when the
// engine event doesn't carry it.
func SinceMs(t time.Time) int64 {
	return time.Since(t).Milliseconds()
}

// persistWait writes a wait row before the wire frame goes out. When
// the translator has no Issuer wired (recovery disabled mode), this is
// a no-op and returns nil. Errors are logged via the emitter's session
// channel and propagated so the caller can decide not to emit.
//
// promptKind is the v2.2 wire frame kind ("question" / "permission" /
// "plan_review"); promptPayload is the payload struct that will be
// emitted. We pre-marshal the full prompt.user frame so reconnect can
// re-emit it byte-for-byte.
func (t *Translator) persistWait(em *emitv2.Emitter, reqID string, kind wait.Kind,
	correlationID, frameType, promptKind string, promptPayload any) error {
	if t.prompter == nil {
		return nil
	}
	frame := buildPromptFrame(em, reqID, frameType, promptKind, promptPayload)
	w := wait.PendingWait{
		RequestID:     reqID,
		SessionID:     em.SessionID(),
		TraceID:       em.TraceID(),
		Kind:          kind,
		CorrelationID: correlationID,
		PromptFrame:   frame,
		Anchor: wait.Anchor{
			AgentPath: em.AgentID(), // "main" or sub-agent id
		},
	}
	return t.prompter.IssueWait(context.Background(), w)
}

// buildPromptFrame produces the same JSON the wire would carry for a
// prompt.user emission, used for reconnect re-emission. We construct it
// directly (rather than capturing what em.PromptUser writes) because
// the Sink-bound emit happens AFTER persist — a chicken-and-egg.
func buildPromptFrame(em *emitv2.Emitter, reqID, frameType, kind string, payload any) []byte {
	frame, err := json.Marshal(map[string]any{
		"type": frameType,
		"envelope": map[string]any{
			"session_id": em.SessionID(),
			"trace_id":   em.TraceID(),
			"agent_id":   em.AgentID(),
		},
		"payload": map[string]any{
			"request_id": reqID,
			"kind":       kind,
			"inner":      payload,
		},
	})
	if err != nil {
		return nil
	}
	return frame
}

// ResolveAskQuestion looks up the engine tool_use_id that an outstanding
// prompt.user(kind=question) request_id corresponds to. conn.go calls
// this on prompt.user_response to bridge back into a tool.result.
// Returns "" when request_id is unknown (i.e. the response is for a
// permission / plan_review prompt, not an upgraded ask_user_question).
//
// Side effect: a successful lookup removes the entry so duplicate
// replies cannot fire the engine twice.
func (t *Translator) ResolveAskQuestion(sessionID, requestID string) string {
	s := t.get(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.askQuestion[requestID]
	if id != "" {
		delete(s.askQuestion, requestID)
		t.resumeForPrompt(s, s.emitter, requestID)
	}
	return id
}

// ResolvePermission looks up the engine PermissionRequest.RequestID
// that an outstanding prompt.user(kind=permission) wire request_id
// corresponds to. Mirror of ResolveAskQuestion / ResolvePlanReview.
//
// Side effect: a successful lookup removes the entry.
func (t *Translator) ResolvePermission(sessionID, requestID string) string {
	s := t.get(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.pendingPerm[requestID]
	if id != "" {
		delete(s.pendingPerm, requestID)
		t.resumeForPrompt(s, s.emitter, requestID)
	}
	return id
}

// ResolvePlanReview looks up the engine plan_id that an outstanding
// prompt.user(kind=plan_review) request_id corresponds to. conn.go
// calls this on prompt.user_response so the synthesised
// types.PlanResponse carries the engine-side plan_id (which is what
// PlanCoordinator's pending-plans map is keyed on). Without this lookup
// the engine cannot match the user's response to the waiting plan and
// PlanCoordinator hangs forever.
//
// Side effect: a successful lookup removes the entry so duplicate
// replies cannot fire the engine twice.
func (t *Translator) ResolvePlanReview(sessionID, requestID string) string {
	s := t.get(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.pendingPlan[requestID]
	if id != "" {
		delete(s.pendingPlan, requestID)
		t.resumeForPrompt(s, s.emitter, requestID)
	}
	return id
}

// ResolveStepDecision is the step_decision counterpart of
// ResolvePlanReview / ResolvePermission. Returns the engine-side
// StepDecisionRequest.RequestID, or "" when this prompt request_id is
// unknown (stale / mismatched).
//
// Side effect: a successful lookup removes the entry and resumes any
// suspended cards.
func (t *Translator) ResolveStepDecision(sessionID, requestID string) string {
	s := t.get(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.pendingStepDecision[requestID]
	if id != "" {
		delete(s.pendingStepDecision, requestID)
		t.resumeForPrompt(s, s.emitter, requestID)
	}
	return id
}

// formatRetryNote renders an LLMRetryInfo as a short human-readable
// line suitable for a card.tick(kind=note) text body. Format mirrors
// the WARN log in retry.Retryer so server-side and wire stay consistent:
//
//	"重试中 (3/10, 1.2s 后再试) — overloaded HTTP 529"
//
// Falls back to attempt-only when classifier didn't tag the error.
func formatRetryNote(info *types.LLMRetryInfo) string {
	if info == nil {
		return ""
	}
	header := fmt.Sprintf(
		"重试中 (%d/%d, %s 后再试)",
		info.Attempt,
		info.MaxRetries,
		time.Duration(info.DelayMs)*time.Millisecond,
	)
	switch {
	case info.ErrorType != "" && info.StatusCode != 0:
		return fmt.Sprintf("%s — %s HTTP %d", header, info.ErrorType, info.StatusCode)
	case info.ErrorType != "":
		return fmt.Sprintf("%s — %s", header, info.ErrorType)
	case info.StatusCode != 0:
		return fmt.Sprintf("%s — HTTP %d", header, info.StatusCode)
	default:
		return header
	}
}

// pickPhrase is a thin wrapper around t.phrasePicker.Pick with nil-safety.
// Returns "" when the picker is not configured.
func (t *Translator) pickPhrase(s *sessionState, toolName string, phase emitv2.ToolPhase, bytes int, retry *toolphrase.RetryInfo) string {
	if t.phrasePicker == nil {
		return ""
	}
	return t.phrasePicker.Pick(s.sessionID, toolName, phase, bytes, retry)
}

// decodeAskQuestionInput pulls fields out of an ask_user_question tool
// input map (whatever shape the LLM produced) into the v2.2
// QuestionPromptPayload schema. Defaults: allow_custom=true, multi=false.
func decodeAskQuestionInput(in map[string]any) (question string, options []emitv2.QuestionOption, multi bool, allowCustom bool) {
	allowCustom = true
	if in == nil {
		return
	}
	if q, ok := in["question"].(string); ok {
		question = q
	}
	if m, ok := in["multi"].(bool); ok {
		multi = m
	}
	if ac, ok := in["allow_custom"].(bool); ok {
		allowCustom = ac
	}
	if rawOpts, ok := in["options"].([]any); ok {
		for _, raw := range rawOpts {
			obj, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			opt := emitv2.QuestionOption{}
			if l, ok := obj["label"].(string); ok {
				opt.Label = l
			}
			if d, ok := obj["description"].(string); ok {
				opt.Description = d
			}
			if opt.Label != "" {
				options = append(options, opt)
			}
		}
	}
	return
}
