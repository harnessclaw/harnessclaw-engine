package websocket

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

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
