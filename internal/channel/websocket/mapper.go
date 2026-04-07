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

	if event.ToolResult != nil {
		output = event.ToolResult.Content
		isError = event.ToolResult.IsError
		if isError {
			status = "error"
		}
		// Copy metadata, extracting duration_ms to a top-level field
		// to avoid duplication in the wire message.
		if event.ToolResult.Metadata != nil {
			metadata = make(map[string]any, len(event.ToolResult.Metadata))
			for k, v := range event.ToolResult.Metadata {
				if k == "duration_ms" {
					switch d := v.(type) {
					case int64:
						durationMs = d
					case float64:
						durationMs = int64(d)
					}
					// Don't copy to metadata — it's promoted to top-level.
					continue
				}
				metadata[k] = v
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
