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
}

// NewEventMapper creates a mapper for the given session.
func NewEventMapper(sessionID string) *EventMapper {
	return &EventMapper{
		sessionID: sessionID,
		startTime: time.Now(),
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
	case types.EngineEventToolStart:
		return m.mapToolStart(event)
	case types.EngineEventToolEnd:
		return m.mapToolEnd(event)
	case types.EngineEventToolCall:
		return m.mapToolCall(event)
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
			OutputTokens: event.Usage.OutputTokens,
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

// --- tool_start (server-side tool execution) ---

func (m *EventMapper) mapToolStart(event *types.EngineEvent) ([][]byte, error) {
	// Close the current text block if open.
	msgs, err := m.closeTextBlock()
	if err != nil {
		return nil, err
	}

	start := ContentStartMessage{
		Type:      MsgTypeContentStart,
		EventID:   newEventID(),
		SessionID: m.sessionID,
		Index:     m.blockIndex,
		ContentBlock: &ContentBlockInfo{
			Type: "tool_use",
			Name: event.ToolName,
		},
	}
	b, err := json.Marshal(start)
	if err != nil {
		return nil, err
	}
	return append(msgs, b), nil
}

// --- tool_end (server-side tool execution) ---

func (m *EventMapper) mapToolEnd(_ *types.EngineEvent) ([][]byte, error) {
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
	m.blockIndex++
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
	if event.Terminal != nil {
		status = mapTerminalReason(event.Terminal.Reason)
		numTurns = event.Terminal.Turn
	}

	taskEnd := TaskEndMessage{
		Type:       MsgTypeTaskEnd,
		EventID:    newEventID(),
		SessionID:  m.sessionID,
		Status:     status,
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
