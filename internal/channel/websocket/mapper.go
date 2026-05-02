package websocket

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"harnessclaw-go/internal/emit"
	"harnessclaw-go/pkg/types"
)

// EventMapper converts EngineEvent values into wire-protocol JSON messages.
// It is stateful: it tracks the current content-block index and whether a
// text block is already open, so callers must use one mapper per session
// turn (and call Reset between turns).
type EventMapper struct {
	sessionID   string
	blockIndex  int
	inTextBlock bool
	startTime   time.Time
	clientTools bool // when true, emit content.* for tool_use blocks (client needs them)
}

// NewEventMapper creates a mapper for the given session.
// clientTools controls whether tool_use content blocks are emitted via content.* events.
// In client-tools mode (true), the client needs content.* events to see what the LLM requested.
// In server-side mode (false), tool.start already carries all the info, so content.* tool_use
// blocks are suppressed to avoid redundancy.
func NewEventMapper(sessionID string, clientTools bool) *EventMapper {
	return &EventMapper{
		sessionID:   sessionID,
		clientTools: clientTools,
		startTime:   time.Now(),
	}
}

// Reset prepares the mapper for a new query-loop turn.
func (m *EventMapper) Reset() {
	m.blockIndex = 0
	m.inTextBlock = false
	m.startTime = time.Now()
}

// Map converts a single EngineEvent into zero or more JSON-encoded messages.
func (m *EventMapper) Map(event *types.EngineEvent) ([][]byte, error) {
	switch event.Type {
	case types.EngineEventMessageStart:
		return m.mapMessageStart(event)
	case types.EngineEventMessageDelta:
		return m.mapMessageDelta(event)
	case types.EngineEventMessageStop:
		return m.mapMessageStop(event)
	case types.EngineEventText:
		return m.mapText(event)
	case types.EngineEventToolUse:
		return m.mapToolUse(event)
	case types.EngineEventToolStart:
		return m.mapToolStart(event)
	case types.EngineEventToolEnd:
		return m.mapToolEnd(event)
	case types.EngineEventToolCall:
		return m.mapToolCall(event)
	case types.EngineEventPermissionRequest:
		return m.mapPermissionRequest(event)
	case types.EngineEventSubAgentStart:
		return m.mapSubAgentStart(event)
	case types.EngineEventSubAgentEnd:
		return m.mapSubAgentEnd(event)
	case types.EngineEventSubAgentEvent:
		return m.mapSubAgentEvent(event)
	case types.EngineEventAgentIntent:
		return m.mapAgentIntent(event)
	case types.EngineEventAgentRouted:
		return m.mapAgentRouted(event)
	case types.EngineEventTaskCreated:
		return m.mapTaskCreated(event)
	case types.EngineEventTaskUpdated:
		return m.mapTaskUpdated(event)
	case types.EngineEventAgentMessage:
		return m.mapAgentMessage(event)
	case types.EngineEventAgentSpawned:
		return m.mapAgentSpawned(event)
	case types.EngineEventAgentIdle:
		return m.mapAgentIdle(event)
	case types.EngineEventAgentCompleted:
		return m.mapAgentCompleted(event)
	case types.EngineEventAgentFailed:
		return m.mapAgentFailed(event)
	case types.EngineEventTeamCreated:
		return m.mapTeamCreated(event)
	case types.EngineEventTeamMemberJoin:
		return m.mapTeamMemberJoin(event)
	case types.EngineEventTeamMemberLeft:
		return m.mapTeamMemberLeft(event)
	case types.EngineEventTeamDeleted:
		return m.mapTeamDeleted(event)
	case types.EngineEventDeliverable:
		return m.mapDeliverable(event)
	case types.EngineEventTraceStarted:
		return m.mapTraceStarted(event)
	case types.EngineEventTraceFinished:
		return m.mapTraceFinished(event)
	case types.EngineEventTraceFailed:
		return m.mapTraceFailed(event)
	case types.EngineEventPlanCreated:
		return m.mapPlanCreated(event)
	case types.EngineEventPlanUpdated:
		return m.mapPlanUpdated(event)
	case types.EngineEventPlanCompleted:
		return m.mapPlanCompleted(event)
	case types.EngineEventPlanFailed:
		return m.mapPlanFailed(event)
	case types.EngineEventStepDispatched:
		return m.mapStepDispatched(event)
	case types.EngineEventStepStarted:
		return m.mapStepStarted(event)
	case types.EngineEventStepProgress:
		return m.mapStepProgress(event)
	case types.EngineEventStepCompleted:
		return m.mapStepCompleted(event)
	case types.EngineEventStepFailed:
		return m.mapStepFailed(event)
	case types.EngineEventStepSkipped:
		return m.mapStepSkipped(event)
	case types.EngineEventAgentHeartbeat:
		return m.mapAgentHeartbeat(event)
	case types.EngineEventError:
		return m.mapError(event)
	case types.EngineEventDone:
		return m.mapDone(event)
	default:
		return nil, nil
	}
}

// --- message lifecycle ---

func (m *EventMapper) mapMessageStart(event *types.EngineEvent) ([][]byte, error) {
	var usage *UsageInfo
	if event.Usage != nil {
		usage = &UsageInfo{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			CacheRead:    event.Usage.CacheRead,
			CacheWrite:   event.Usage.CacheWrite,
		}
	}

	msg := MessageStartMessage{
		Type:      MsgTypeMessageStart,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Message: MessageStartInfo{
			ID:    event.MessageID,
			Model: event.Model,
			Role:  "assistant",
			Usage: usage,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

func (m *EventMapper) mapMessageDelta(event *types.EngineEvent) ([][]byte, error) {
	// Close any open text block first.
	msgs, err := m.closeTextBlock()
	if err != nil {
		return nil, err
	}

	var usage *UsageInfo
	if event.Usage != nil {
		usage = &UsageInfo{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			CacheRead:    event.Usage.CacheRead,
			CacheWrite:   event.Usage.CacheWrite,
		}
	}

	msg := MessageDeltaMessage{
		Type:      MsgTypeMessageDelta,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Delta: MessageDeltaInfo{
			StopReason: event.StopReason,
		},
		Usage: usage,
	}

	// Attach error detail when the stop reason is "error".
	if event.Error != nil {
		msg.Delta.Error = &ErrorDetail{
			Type:    "model_error",
			Code:    "model_error",
			Message: event.Error.Error(),
		}
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(msgs, b), nil
}

func (m *EventMapper) mapMessageStop(_ *types.EngineEvent) ([][]byte, error) {
	msg := MessageStopMessage{
		Type:      MsgTypeMessageStop,
		EventID:   newEventID(),
		SessionID: m.sessionID,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- text ---

func (m *EventMapper) mapText(event *types.EngineEvent) ([][]byte, error) {
	var msgs [][]byte

	// If no text block is open yet, emit a content.start first.
	if !m.inTextBlock {
		start := ContentStartMessage{
			Type:      MsgTypeContentStart,
			EventID:   newEventID(),
			SessionID: m.sessionID,
			Index:     m.blockIndex,
			ContentBlock: &ContentBlockInfo{
				Type: "text",
			},
		}
		b, err := json.Marshal(start)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, b)
		m.inTextBlock = true
	}

	// Emit the delta.
	delta := ContentDeltaMessage{
		Type:      MsgTypeContentDelta,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Index:     m.blockIndex,
		Delta: &Delta{
			Type: "text_delta",
			Text: event.Text,
		},
	}
	b, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, b)
	return msgs, nil
}

// --- tool_use (LLM requested a tool call — content block from LLM output) ---

func (m *EventMapper) mapToolUse(event *types.EngineEvent) ([][]byte, error) {
	// Close the current text block if open.
	msgs, err := m.closeTextBlock()
	if err != nil {
		return nil, err
	}

	// In server-side mode, tool.start already carries tool name/id/input,
	// so we skip emitting content.* for tool_use blocks to avoid redundancy.
	if !m.clientTools {
		return msgs, nil
	}

	// Emit content.start for the tool_use block.
	start := ContentStartMessage{
		Type:      MsgTypeContentStart,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Index:     m.blockIndex,
		ContentBlock: &ContentBlockInfo{
			Type: "tool_use",
			ID:   event.ToolUseID,
			Name: event.ToolName,
		},
	}
	b, err := json.Marshal(start)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, b)

	// Emit content.delta with the tool input JSON.
	if event.ToolInput != "" {
		delta := ContentDeltaMessage{
			Type:      MsgTypeContentDelta,
			EventID:   newEventID(),
			SessionID: m.sessionID,
			Index:     m.blockIndex,
			Delta: &Delta{
				Type:        "input_json_delta",
				PartialJSON: event.ToolInput,
			},
		}
		b, err = json.Marshal(delta)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, b)
	}

	// Emit content.stop to close the tool_use content block.
	stop := ContentStopMessage{
		Type:      MsgTypeContentStop,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Index:     m.blockIndex,
	}
	b, err = json.Marshal(stop)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, b)
	m.blockIndex++

	return msgs, nil
}

// --- tool.start (server-side tool execution begins) ---

func (m *EventMapper) mapToolStart(event *types.EngineEvent) ([][]byte, error) {
	var input map[string]interface{}
	if event.ToolInput != "" {
		if err := json.Unmarshal([]byte(event.ToolInput), &input); err != nil {
			input = map[string]interface{}{"raw": event.ToolInput}
		}
	}

	msg := ToolStartMessage{
		Type:      MsgTypeToolStart,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		ToolUseID: event.ToolUseID,
		ToolName:  event.ToolName,
		Input:     input,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- tool.end (server-side tool execution completed) ---

func (m *EventMapper) mapToolEnd(event *types.EngineEvent) ([][]byte, error) {
	status := "success"
	output := ""
	isError := false
	var metadata map[string]any
	var durationMs int64
	var renderHint RenderHint
	var language, filePath string

	if event.ToolResult != nil {
		output = event.ToolResult.Content
		isError = event.ToolResult.IsError
		if isError {
			status = "error"
		}
		// Copy metadata, promoting well-known keys to top-level fields.
		if event.ToolResult.Metadata != nil {
			metadata = make(map[string]any, len(event.ToolResult.Metadata))
			for k, v := range event.ToolResult.Metadata {
				switch k {
				case "duration_ms":
					switch d := v.(type) {
					case int64:
						durationMs = d
					case float64:
						durationMs = int64(d)
					}
					continue
				case MetaRenderHint:
					if s, ok := v.(string); ok {
						renderHint = RenderHint(s)
					}
					continue
				case MetaLanguage:
					if s, ok := v.(string); ok {
						language = s
					}
					continue
				case MetaFilePath:
					if s, ok := v.(string); ok {
						filePath = s
					}
					continue
				default:
					metadata[k] = v
				}
			}
			if len(metadata) == 0 {
				metadata = nil
			}
		}
	}

	msg := ToolEndMessage{
		Type:       MsgTypeToolEnd,
		EventID:    newEventID(),
		SessionID:  m.sessionID,
		ToolUseID:  event.ToolUseID,
		ToolName:   event.ToolName,
		Status:     status,
		Output:     output,
		IsError:    isError,
		DurationMs: durationMs,
		RenderHint: renderHint,
		Language:   language,
		FilePath:   filePath,
		Metadata:   metadata,
		// Surface produced artifacts on the wire so the frontend can
		// render artifact cards without parsing tool result JSON. The
		// engine-side executor populates event.Artifacts from
		// metadata["artifacts"] (Task/Specialists aggregate) or from the
		// render_hint=artifact extraction path (single ArtifactWrite).
		// Without this assignment the Refs land in the engine event but
		// vanish before reaching the wire — the bug operators noticed
		// when "subagent calls produced artifacts but UI was empty".
		Artifacts: event.Artifacts,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- tool_call (client-side tool execution) ---

func (m *EventMapper) mapToolCall(event *types.EngineEvent) ([][]byte, error) {
	// Parse the tool input JSON string into a map for the wire protocol.
	var input map[string]interface{}
	if event.ToolInput != "" {
		if err := json.Unmarshal([]byte(event.ToolInput), &input); err != nil {
			// If parsing fails, wrap the raw string.
			input = map[string]interface{}{"raw": event.ToolInput}
		}
	}

	msg := ToolCallMessage{
		Type:      MsgTypeToolCall,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		ToolUseID: event.ToolUseID,
		ToolName:  event.ToolName,
		Input:     input,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- permission_request (server asks client for tool approval) ---

func (m *EventMapper) mapPermissionRequest(event *types.EngineEvent) ([][]byte, error) {
	if event.PermissionRequest == nil {
		return nil, nil
	}

	// Convert options from types to wire format.
	var wireOpts []PermissionOptionWire
	for _, opt := range event.PermissionRequest.Options {
		wireOpts = append(wireOpts, PermissionOptionWire{
			Label: opt.Label,
			Scope: string(opt.Scope),
			Allow: opt.Allow,
		})
	}

	msg := PermissionRequestMessage{
		Type:          MsgTypePermissionRequest,
		EventID:       newEventID(),
		SessionID:     m.sessionID,
		RequestID:     event.PermissionRequest.RequestID,
		ToolName:      event.PermissionRequest.ToolName,
		ToolInput:     event.PermissionRequest.ToolInput,
		Message:       event.PermissionRequest.Message,
		IsReadOnly:    event.PermissionRequest.IsReadOnly,
		Options:       wireOpts,
		PermissionKey: event.PermissionRequest.PermissionKey,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- subagent_start (sub-agent session begins) ---

func (m *EventMapper) mapSubAgentStart(event *types.EngineEvent) ([][]byte, error) {
	msg := SubAgentStartMessage{
		Type:          MsgTypeSubAgentStart,
		EventID:       newEventID(),
		SessionID:     m.sessionID,
		AgentID:       event.AgentID,
		AgentName:     event.AgentName,
		Description:   event.AgentDesc,
		Task:          event.AgentTask,
		AgentType:     event.AgentType,
		ParentAgentID: event.ParentAgentID,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- subagent_end (sub-agent session completes) ---

func (m *EventMapper) mapSubAgentEnd(event *types.EngineEvent) ([][]byte, error) {
	var usage *UsageInfo
	if event.Usage != nil {
		usage = &UsageInfo{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			CacheRead:    event.Usage.CacheRead,
			CacheWrite:   event.Usage.CacheWrite,
		}
	}
	msg := SubAgentEndMessage{
		Type:        MsgTypeSubAgentEnd,
		EventID:     newEventID(),
		SessionID:   m.sessionID,
		AgentID:     event.AgentID,
		AgentName:   event.AgentName,
		Status:      event.AgentStatus,
		DurationMs:  event.Duration,
		Usage:       usage,
		DeniedTools: event.DeniedTools,
		// Aggregated produced artifacts (v1.13+). The engine populates
		// this from SpawnResult.SubmittedArtifacts (contract mode) or
		// the broader ArtifactWrite collection (legacy). Same wire-drop
		// bug as ToolEnd above — fixed here.
		Artifacts: event.Artifacts,
	}
	if event.Terminal != nil {
		msg.NumTurns = event.Terminal.Turn
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_intent (per-tool progress sentence) ---

// mapAgentIntent emits a top-level agent.intent frame for the main agent.
// Sub-agent intents arrive wrapped as subagent_event{event_type=intent} (see
// SpawnSync forwarding loop) and go through mapSubAgentEvent instead, so we
// don't need a sub-agent path here.
func (m *EventMapper) mapAgentIntent(event *types.EngineEvent) ([][]byte, error) {
	msg := AgentIntentMessage{
		Type:      MsgTypeAgentIntent,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		AgentID:   event.AgentID,
		AgentName: event.AgentName,
		ToolUseID: event.ToolUseID,
		ToolName:  event.ToolName,
		Intent:    event.Intent,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- subagent_event (real-time sub-agent streaming) ---

func (m *EventMapper) mapSubAgentEvent(event *types.EngineEvent) ([][]byte, error) {
	if event.SubAgentEvent == nil {
		return nil, nil
	}
	msg := SubAgentEventMessage{
		Type:      MsgTypeSubAgentEvent,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		AgentID:   event.AgentID,
		AgentName: event.AgentName,
		Payload:   event.SubAgentEvent,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_routed (@-mention routing) ---

func (m *EventMapper) mapAgentRouted(event *types.EngineEvent) ([][]byte, error) {
	msg := AgentRoutedMessage{
		Type:      MsgTypeAgentRouted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		AgentName: event.AgentName,
		Description: event.AgentDesc,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- task_created ---

func (m *EventMapper) mapTaskCreated(event *types.EngineEvent) ([][]byte, error) {
	if event.TaskEvent == nil {
		return nil, nil
	}
	msg := TaskCreatedMessage{
		Type:      MsgTypeTaskCreated,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Task: TaskInfoWire{
			TaskID:  event.TaskEvent.TaskID,
			Subject: event.TaskEvent.Subject,
			Status:  event.TaskEvent.Status,
			Owner:   event.TaskEvent.Owner,
			ScopeID: event.TaskEvent.ScopeID,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- task_updated ---

func (m *EventMapper) mapTaskUpdated(event *types.EngineEvent) ([][]byte, error) {
	if event.TaskEvent == nil {
		return nil, nil
	}
	msg := TaskUpdatedMessage{
		Type:      MsgTypeTaskUpdated,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Task: TaskInfoWire{
			TaskID:     event.TaskEvent.TaskID,
			Subject:    event.TaskEvent.Subject,
			Status:     event.TaskEvent.Status,
			Owner:      event.TaskEvent.Owner,
			ActiveForm: event.TaskEvent.ActiveForm,
			ScopeID:    event.TaskEvent.ScopeID,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_message (inter-agent) ---

func (m *EventMapper) mapAgentMessage(event *types.EngineEvent) ([][]byte, error) {
	if event.AgentMsg == nil {
		return nil, nil
	}
	msg := AgentMessageWireMessage{
		Type:      MsgTypeAgentMessage,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Message: AgentMsgInfoWire{
			From:    event.AgentMsg.From,
			To:      event.AgentMsg.To,
			Summary: event.AgentMsg.Summary,
			TeamID:  event.AgentMsg.TeamID,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_spawned (async agent launched) ---

func (m *EventMapper) mapAgentSpawned(event *types.EngineEvent) ([][]byte, error) {
	msg := AgentSpawnedMessage{
		Type:          MsgTypeAgentSpawned,
		EventID:       newEventID(),
		SessionID:     m.sessionID,
		AgentID:       event.AgentID,
		AgentName:     event.AgentName,
		Description:   event.AgentDesc,
		AgentType:     event.AgentType,
		ParentAgentID: event.ParentAgentID,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_idle ---

func (m *EventMapper) mapAgentIdle(event *types.EngineEvent) ([][]byte, error) {
	msg := AgentIdleMessage{
		Type:      MsgTypeAgentIdle,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		AgentID:   event.AgentID,
		AgentName: event.AgentName,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_completed (async agent done) ---

func (m *EventMapper) mapAgentCompleted(event *types.EngineEvent) ([][]byte, error) {
	var usage *UsageInfo
	if event.Usage != nil {
		usage = &UsageInfo{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			CacheRead:    event.Usage.CacheRead,
			CacheWrite:   event.Usage.CacheWrite,
		}
	}
	msg := AgentCompletedMessage{
		Type:       MsgTypeAgentCompleted,
		EventID:    newEventID(),
		SessionID:  m.sessionID,
		AgentID:    event.AgentID,
		AgentName:  event.AgentName,
		Status:     event.AgentStatus,
		DurationMs: event.Duration,
		Usage:      usage,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- agent_failed (async agent error) ---

func (m *EventMapper) mapAgentFailed(event *types.EngineEvent) ([][]byte, error) {
	errMsg := "unknown error"
	if event.Error != nil {
		errMsg = event.Error.Error()
	}
	msg := AgentFailedMessage{
		Type:       MsgTypeAgentFailed,
		EventID:    newEventID(),
		SessionID:  m.sessionID,
		AgentID:    event.AgentID,
		AgentName:  event.AgentName,
		Error:      ErrorDetail{Type: "agent_error", Code: "agent_failed", Message: errMsg},
		DurationMs: event.Duration,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- team_created ---

func (m *EventMapper) mapTeamCreated(event *types.EngineEvent) ([][]byte, error) {
	if event.TeamEvent == nil {
		return nil, nil
	}
	msg := TeamCreatedMessage{
		Type:      MsgTypeTeamCreated,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Team: TeamInfoWire{
			TeamID:   event.TeamEvent.TeamID,
			TeamName: event.TeamEvent.TeamName,
			Members:  event.TeamEvent.Members,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- team_member_join ---

func (m *EventMapper) mapTeamMemberJoin(event *types.EngineEvent) ([][]byte, error) {
	if event.TeamEvent == nil {
		return nil, nil
	}
	msg := TeamMemberJoinMessage{
		Type:      MsgTypeTeamMemberJoin,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Team: TeamInfoWire{
			TeamID:     event.TeamEvent.TeamID,
			TeamName:   event.TeamEvent.TeamName,
			Members:    event.TeamEvent.Members,
			MemberName: event.TeamEvent.MemberName,
			MemberType: event.TeamEvent.MemberType,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- team_member_left ---

func (m *EventMapper) mapTeamMemberLeft(event *types.EngineEvent) ([][]byte, error) {
	if event.TeamEvent == nil {
		return nil, nil
	}
	msg := TeamMemberLeftMessage{
		Type:      MsgTypeTeamMemberLeft,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Team: TeamInfoWire{
			TeamID:     event.TeamEvent.TeamID,
			TeamName:   event.TeamEvent.TeamName,
			Members:    event.TeamEvent.Members,
			MemberName: event.TeamEvent.MemberName,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- team_deleted ---

func (m *EventMapper) mapTeamDeleted(event *types.EngineEvent) ([][]byte, error) {
	if event.TeamEvent == nil {
		return nil, nil
	}
	msg := TeamDeletedMessage{
		Type:      MsgTypeTeamDeleted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Team: TeamInfoWire{
			TeamID:   event.TeamEvent.TeamID,
			TeamName: event.TeamEvent.TeamName,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- error ---

func (m *EventMapper) mapError(event *types.EngineEvent) ([][]byte, error) {
	errMsg := ""
	if event.Error != nil {
		errMsg = event.Error.Error()
	}

	errMessage := ErrorMessage{
		Type:      MsgTypeError,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Error: ErrorDetail{
			Type:    "internal_error",
			Code:    "engine_error",
			Message: errMsg,
		},
	}
	b, err := json.Marshal(errMessage)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- done ---

func (m *EventMapper) mapDone(event *types.EngineEvent) ([][]byte, error) {
	// Close any open text block before emitting task.end.
	msgs, err := m.closeTextBlock()
	if err != nil {
		return nil, err
	}

	status := "success"
	numTurns := 0
	message := ""
	if event.Terminal != nil {
		status = mapTerminalReason(event.Terminal.Reason)
		numTurns = event.Terminal.Turn
		message = event.Terminal.Message
	}

	taskEnd := TaskEndMessage{
		Type:       MsgTypeTaskEnd,
		EventID:    newEventID(),
		SessionID:  m.sessionID,
		Status:     status,
		Message:    message,
		DurationMs: time.Since(m.startTime).Milliseconds(),
		NumTurns:   numTurns,
	}

	if event.Usage != nil {
		taskEnd.TotalUsage = &UsageInfo{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			CacheRead:    event.Usage.CacheRead,
			CacheWrite:   event.Usage.CacheWrite,
		}
	}

	b, err := json.Marshal(taskEnd)
	if err != nil {
		return nil, err
	}
	return append(msgs, b), nil
}

// --- deliverable ---

func (m *EventMapper) mapDeliverable(event *types.EngineEvent) ([][]byte, error) {
	if event.Deliverable == nil {
		return nil, nil
	}

	msg := DeliverableReadyMessage{
		Type:      MsgTypeDeliverableReady,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		AgentID:   event.AgentID,
		AgentName: event.AgentName,
		FilePath:  event.Deliverable.FilePath,
		Language:  event.Deliverable.Language,
		ByteSize:  event.Deliverable.ByteSize,
	}

	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- emit lifecycle events (v1.11) ---

// mapTraceStarted converts a trace_started engine event to a trace.started
// wire message. Carries the user input summary so the client can echo it
// while later events stream in.
func (m *EventMapper) mapTraceStarted(event *types.EngineEvent) ([][]byte, error) {
	msg := TraceStartedMessage{
		Type:      MsgTypeTraceStarted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload: TraceStartedPayload{
			UserInputSummary: event.Text,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// mapTraceFinished maps trace_finished — emitted at the end of a successful
// query loop. Metrics carries the cumulative token/duration totals.
func (m *EventMapper) mapTraceFinished(event *types.EngineEvent) ([][]byte, error) {
	msg := TraceFinishedMessage{
		Type:      MsgTypeTraceFinished,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload: TraceFinishedPayload{
			OutputSummary: event.Text,
		},
	}
	if event.Terminal != nil {
		msg.Payload.NumTurns = event.Terminal.Turn
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// mapTraceFailed maps trace_failed — emitted when the query loop ends
// abnormally. The error block carries both developer-facing detail and a
// user-facing fallback for L1 to relay in persona.
func (m *EventMapper) mapTraceFailed(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	body := buildErrorBody(td, event)
	msg := TraceFailedMessage{
		Type:      MsgTypeTraceFailed,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload:   FailurePayload{Error: body},
	}
	return marshalOne(msg)
}

// mapPlanCreated maps plan_created — the L2 planner produced and accepted
// the task graph.
func (m *EventMapper) mapPlanCreated(event *types.EngineEvent) ([][]byte, error) {
	msg := PlanCreatedMessage{
		Type:      MsgTypePlanCreated,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload:   planPayloadFromTypes(event.PlanEvent),
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// mapPlanUpdated maps plan_updated — emitted when re-planning replaces the
// in-flight graph (e.g. after a step failure forced a revision).
func (m *EventMapper) mapPlanUpdated(event *types.EngineEvent) ([][]byte, error) {
	msg := PlanUpdatedMessage{
		Type:      MsgTypePlanUpdated,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload:   planPayloadFromTypes(event.PlanEvent),
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// mapPlanCompleted maps plan_completed — every step in the plan is in a
// terminal state (completed/failed/skipped).
func (m *EventMapper) mapPlanCompleted(event *types.EngineEvent) ([][]byte, error) {
	msg := PlanCompletedMessage{
		Type:      MsgTypePlanCompleted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload:   planPayloadFromTypes(event.PlanEvent),
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// planPayloadFromTypes copies an internal PlanEvent into the wire payload.
// Returns the zero PlanPayload when pe is nil (so callers don't have to
// guard on the nil case).
func planPayloadFromTypes(pe *types.PlanEvent) PlanPayload {
	if pe == nil {
		return PlanPayload{}
	}
	tasks := make([]PlanTaskInfoWire, 0, len(pe.Tasks))
	for _, t := range pe.Tasks {
		tasks = append(tasks, PlanTaskInfoWire{
			TaskID:            t.TaskID,
			SubagentType:      t.SubagentType,
			DependsOn:         t.DependsOn,
			UserFacingTitle:   t.UserFacingTitle,
			UserFacingSummary: t.UserFacingSummary,
		})
	}
	return PlanPayload{
		PlanID:   pe.PlanID,
		Goal:     pe.Goal,
		Strategy: pe.Strategy,
		Status:   pe.Status,
		Tasks:    tasks,
	}
}

// mapStepDispatched maps step_dispatched — orchestrator sent a step to
// a worker.
func (m *EventMapper) mapStepDispatched(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	msg := StepDispatchedMessage{
		Type:      MsgTypeStepDispatched,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload: StepDispatchedPayload{
			StepID:       td.TaskID,
			SubagentType: td.SubagentType,
			AgentID:      td.AgentID,
			InputSummary: td.InputSummary,
		},
	}
	return marshalOne(msg)
}

// mapStepStarted maps step_started — the worker assigned to a dispatched
// step has actually begun execution.
func (m *EventMapper) mapStepStarted(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	msg := StepStartedMessage{
		Type:      MsgTypeStepStarted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload: StepStartedPayload{
			StepID:  td.TaskID,
			AgentID: td.AgentID,
		},
	}
	return marshalOne(msg)
}

// mapStepProgress maps step_progress — incremental progress for a long
// step. Producers MUST throttle these to avoid flooding the client.
func (m *EventMapper) mapStepProgress(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	msg := StepProgressMessage{
		Type:      MsgTypeStepProgress,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload: StepProgressPayload{
			StepID: td.TaskID,
		},
	}
	return marshalOne(msg)
}

// mapStepCompleted maps step_completed — worker returned a successful
// result.
func (m *EventMapper) mapStepCompleted(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	msg := StepCompletedMessage{
		Type:      MsgTypeStepCompleted,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload: StepCompletedPayload{
			StepID:        td.TaskID,
			OutputSummary: td.OutputSummary,
			Attempts:      td.Attempts,
			Deliverables:  td.Deliverables,
		},
	}
	return marshalOne(msg)
}

// mapStepFailed maps step_failed — worker reported failure or returned
// non-completed terminal status (e.g. max_turns). The error block is
// shaped per §6.12 ErrorDetail (type required) so monitoring rules can
// match across emit and connection-level errors uniformly.
func (m *EventMapper) mapStepFailed(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	body := buildErrorBody(td, event)
	msg := StepFailedMessage{
		Type:      MsgTypeStepFailed,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload:   FailurePayload{Error: body},
	}
	return marshalOne(msg)
}

// mapStepSkipped maps step_skipped — emitted when a step is skipped,
// most commonly because an upstream dependency failed.
func (m *EventMapper) mapStepSkipped(event *types.EngineEvent) ([][]byte, error) {
	td := stepDispatchOrEmpty(event)
	msg := StepSkippedMessage{
		Type:      MsgTypeStepSkipped,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Payload: StepSkippedPayload{
			StepID: td.TaskID,
			Reason: td.Reason,
		},
	}
	return marshalOne(msg)
}

// mapPlanFailed maps plan_failed — emitted when the orchestrator could
// not produce a usable plan or every step in the plan failed.
func (m *EventMapper) mapPlanFailed(event *types.EngineEvent) ([][]byte, error) {
	td := &types.TaskDispatch{}
	if event.TaskDispatch != nil {
		td = event.TaskDispatch
	}
	body := buildErrorBody(td, event)
	msg := PlanFailedMessage{
		Type:      MsgTypePlanFailed,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Display:   displayFromTypes(event.Display),
		Metrics:   metricsFromTypes(event.Metrics),
		Payload:   FailurePayload{Error: body},
	}
	return marshalOne(msg)
}

// stepDispatchOrEmpty returns event.TaskDispatch or a zero value, so
// mapper functions don't have to nil-check repeatedly.
func stepDispatchOrEmpty(event *types.EngineEvent) *types.TaskDispatch {
	if event.TaskDispatch != nil {
		return event.TaskDispatch
	}
	return &types.TaskDispatch{}
}

// buildErrorBody assembles a §6.12-shaped ErrorBody from a TaskDispatch
// (developer-facing emit data) plus the engine event (fallback for
// generic error). Type defaults to internal_error when the producer
// didn't set one — this keeps the wire schema valid even from naive
// callers.
func buildErrorBody(td *types.TaskDispatch, event *types.EngineEvent) ErrorBody {
	body := ErrorBody{
		Type:        td.ErrorType,
		Code:        td.ErrorCode,
		Message:     td.Error,
		UserMessage: td.UserMessage,
		Retryable:   td.Retryable,
	}
	if body.Message == "" && event.Error != nil {
		body.Message = event.Error.Error()
	}
	if body.Type == "" {
		body.Type = string(emit.ErrorTypeInternal)
	}
	return body
}

// marshalOne is a tiny convenience that returns a single-element [][]byte
// or an error — every emit mapper looks the same so this removes 6 lines
// of boilerplate per function.
func marshalOne(v any) ([][]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// mapAgentHeartbeat maps agent_heartbeat — proves a long-running agent is
// still alive.
func (m *EventMapper) mapAgentHeartbeat(event *types.EngineEvent) ([][]byte, error) {
	msg := AgentHeartbeatMessage{
		Type:      MsgTypeAgentHeartbeat,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Envelope:  envelopeFromTypes(event.Envelope),
		Payload: AgentHeartbeatPayload{
			AgentID:  event.AgentID,
			UptimeMs: event.Duration,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

// --- helpers ---

// closeTextBlock emits a content.stop for the current text block if one
// is open, and increments the block index.
func (m *EventMapper) closeTextBlock() ([][]byte, error) {
	if !m.inTextBlock {
		return nil, nil
	}
	stop := ContentStopMessage{
		Type:      MsgTypeContentStop,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Index:     m.blockIndex,
	}
	b, err := json.Marshal(stop)
	if err != nil {
		return nil, err
	}
	m.inTextBlock = false
	m.blockIndex++
	return [][]byte{b}, nil
}

// mapTerminalReason converts a TerminalReason to the task.end status string.
func mapTerminalReason(r types.TerminalReason) string {
	switch r {
	case types.TerminalCompleted:
		return "success"
	case types.TerminalMaxTurns:
		return "error_max_turns"
	case types.TerminalModelError:
		return "error_model"
	case types.TerminalAbortedStreaming, types.TerminalAbortedTools:
		return "aborted"
	default:
		return "error"
	}
}

func newEventID() string {
	return "evt_" + uuid.New().String()[:8]
}
