package websocket

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/internal/engine/wait"
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
}

// promptIssuer is the minimal slice of prompter.Prompter the translator
// needs. Defined as an interface so tests can inject a fake without
// pulling in the full prompter package.
type promptIssuer interface {
	IssueWait(ctx context.Context, w wait.PendingWait) error
}

// NewTranslator constructs an empty Translator.
func NewTranslator() *Translator {
	return &Translator{sessions: make(map[string]*sessionState)}
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

	turnCardID    string
	turnNo        int
	messageCardID string
	tools         map[string]string         // tool_use_id → tool card_id
	subagents     map[string]*emitv2.Emitter // agent_id → child Emitter (sub-agent scope)
	subAgentCard  map[string]string         // agent_id → agent card_id
	plans         map[string]string         // plan_id → plan card_id
	steps         map[string]string         // step_id → step card_id
	pendingPerm   map[string]string         // request_id → ⟨request_id⟩ (for prompt.reply correlation)

	// askQuestion maps a v2.2 prompt.user request_id back to the
	// originating engine tool_use_id. AskUserQuestion is a client-routed
	// tool: the engine's tool executor blocks on a tool.result. v2.2
	// surfaces it as prompt.user(kind=question) instead of card.add(tool);
	// when the user replies with prompt.user_response, conn.go uses this
	// map to find the tool_use_id and dispatch a tool.result that
	// unblocks the engine.
	askQuestion map[string]string // prompt request_id → tool_use_id

	// pendingPlan maps a v2.2 prompt.user request_id to the engine's
	// PlanCoordinator plan_id. Same pattern as askQuestion: the engine
	// blocks on SubmitPlanResponse keyed by plan_id, but on the wire
	// we use a v2.2 request_id. conn.go uses this map to bridge back
	// — without it, the user's prompt.user_response carries a synthetic
	// request_id the engine doesn't recognise and PlanCoordinator
	// hangs forever.
	pendingPlan map[string]string // prompt request_id → engine plan_id
}

func newSessionState() *sessionState {
	return &sessionState{
		tools:        make(map[string]string),
		subagents:    make(map[string]*emitv2.Emitter),
		subAgentCard: make(map[string]string),
		plans:        make(map[string]string),
		steps:        make(map[string]string),
		pendingPerm:  make(map[string]string),
		askQuestion:  make(map[string]string),
		pendingPlan:  make(map[string]string),
	}
}

// askUserQuestionToolName mirrors internal/tool/askuserquestion.ToolName.
// Hardcoded here to avoid an import cycle.
const askUserQuestionToolName = "AskUserQuestion"

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
	t.sessions[sessionID] = s
	return s
}

// Drop releases per-session translation state. Call when a session
// closes so memory doesn't grow unbounded on long-lived servers.
func (t *Translator) Drop(sessionID string) {
	t.mu.Lock()
	delete(t.sessions, sessionID)
	t.mu.Unlock()
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

	switch ev.Type {
	// ----- Message lifecycle -----
	case types.EngineEventMessageStart:
		t.openTurnIfNeeded(s, em)
		mid := nonEmpty(ev.MessageID, "msg_"+emitv2.NewCardID(emitv2.CardMessage))
		s.messageCardID = mid
		em.Card(emitv2.CardMessage, mid).Add(emitv2.MessagePayload{
			Role:  "assistant",
			Model: ev.Model,
		}, emitv2.WithParent(s.turnCardID))

	case types.EngineEventText:
		t.openTurnIfNeeded(s, em)
		t.openMessageIfNeeded(s, em, "")
		em.Card(emitv2.CardMessage, s.messageCardID).Append(emitv2.ChannelText, ev.Text)

	case types.EngineEventToolUse:
		// LLM signalled it wants to call a tool. The tool input streams
		// as ChannelToolInput on the same message; the actual tool card
		// opens later on EngineEventToolStart. No-op here — the input
		// already arrived through message stream consumers upstream.

	case types.EngineEventMessageDelta:
		// Carries stop_reason + usage. We attach via Set on the open message.
		if s.messageCardID == "" {
			return
		}
		em.Card(emitv2.CardMessage, s.messageCardID).Set(map[string]any{
			"stop_reason": ev.StopReason,
		})

	case types.EngineEventMessageStop:
		if s.messageCardID == "" {
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
		em.Card(emitv2.CardMessage, s.messageCardID).Close(emitv2.StatusOK, opts...)
		s.messageCardID = ""

	// ----- Tool lifecycle -----
	case types.EngineEventToolStart:
		t.openTurnIfNeeded(s, em)
		toolCardID := nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[ev.ToolUseID] = toolCardID
		input := parseJSONObject(ev.ToolInput)
		em.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:   ev.ToolName,
			Target: "server",
			Intent: ev.Intent,
			Input:  input,
		}, emitv2.WithParent(parentForTool(s)))

	case types.EngineEventToolEnd:
		toolCardID, ok := s.tools[ev.ToolUseID]
		if !ok {
			toolCardID = nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		}
		delete(s.tools, ev.ToolUseID)
		var inner emitv2.ToolPayload
		var status = emitv2.StatusOK
		var errInfo *emitv2.ErrorInfo
		if ev.ToolResult != nil {
			inner.Output = ev.ToolResult.Content
			if ev.ToolResult.IsError {
				status = emitv2.StatusFailed
				errInfo = emitv2.NewError(emitv2.ErrorTypeInternal, ev.ToolResult.Content)
			}
			if rh, ok := ev.ToolResult.Metadata["render_hint"].(string); ok {
				inner.RenderHint = rh
			}
			if lang, ok := ev.ToolResult.Metadata["language"].(string); ok {
				inner.Language = lang
			}
			if fp, ok := ev.ToolResult.Metadata["file_path"].(string); ok {
				inner.FilePath = fp
			}
		}
		opts := []emitv2.EmitOpt{emitv2.WithInner(inner)}
		if errInfo != nil {
			opts = append(opts, emitv2.WithError(errInfo))
		}
		em.Card(emitv2.CardTool, toolCardID).Close(status, opts...)

	case types.EngineEventToolCall:
		// Client-side tool execution. Two paths:
		//   (a) AskUserQuestion → upgrade to prompt.user(kind=question)
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
			return
		}
		toolCardID := nonEmpty(ev.ToolUseID, emitv2.NewCardID(emitv2.CardTool))
		s.tools[ev.ToolUseID] = toolCardID
		em.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:   ev.ToolName,
			Target: "client",
			Intent: ev.Intent,
			Input:  input,
		}, emitv2.WithParent(parentForTool(s)))

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
		parent := parentForSubAgent(s, ev.ParentAgentID)
		child.Card(emitv2.CardAgent, agentCardID).Add(emitv2.AgentPayload{
			Name:          ev.AgentName,
			AgentType:     ev.AgentType,
			ParentAgentID: ev.ParentAgentID,
			TaskPrompt:    ev.AgentTask,
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
				NumTurns:    safeTurnCount(ev),
				DeniedTools: ev.DeniedTools,
				Artifacts:   refs,
			}),
		}
		if metrics != nil {
			opts = append(opts, emitv2.WithMetrics(*metrics))
		}
		status := emitv2.StatusOK
		if ev.AgentStatus == "error" || ev.AgentStatus == "failed" {
			status = emitv2.StatusFailed
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

	case types.EngineEventPlanApproved:
		if ev.PlanProposal != nil {
			em.PromptReply("", "approved", "")
		}

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
		errInfo := emitv2.NewError(emitv2.ErrorTypeInternal, errMsg(ev.Error))
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

// parentForTool decides what card a tool attaches to. Prefer the most
// recent open message (causal — tool was requested in a message),
// falling back to the turn.
func parentForTool(s *sessionState) string {
	if s.messageCardID != "" {
		return s.messageCardID
	}
	return s.turnCardID
}

// parentForSubAgent decides what card a sub-agent attaches to. The
// sub-agent typically spawns from a tool call (Agent / Specialists);
// if the tool card is open, attach there. Otherwise attach to the
// parent agent's card or the turn.
func parentForSubAgent(s *sessionState, parentAgentID string) string {
	// If the parent agent has a card open, attach there.
	if id := s.subAgentCard[parentAgentID]; id != "" {
		return id
	}
	// Else attach to the most recent tool card (Agent/Specialists call).
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
		child.Card(emitv2.CardTool, toolCardID).Add(emitv2.ToolPayload{
			Name:   inner.ToolName,
			Target: "server",
			Input:  input,
		}, emitv2.WithParent(s.subAgentCard[ev.AgentID]))
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
	}, emitv2.WithParent(s.turnCardID))
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
// permission / plan_review prompt, not an upgraded AskUserQuestion).
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
	}
	return id
}

// decodeAskQuestionInput pulls fields out of an AskUserQuestion tool
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
