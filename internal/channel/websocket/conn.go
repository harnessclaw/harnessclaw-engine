package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
	"nhooyr.io/websocket"

	emitv2 "harnessclaw-go/internal/emit/v2"
	"harnessclaw-go/internal/engine/wait"
	"harnessclaw-go/pkg/types"
)

const (
	sendBufSize     = 256
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
)

// Conn is one live WebSocket connection. Created by Channel.upgrade,
// owns its read/write pumps and an Emitter bound to the session.
type Conn struct {
	id     string
	ws     *websocket.Conn
	send   chan []byte
	done   chan struct{}
	ch     *Channel
	logger *zap.Logger

	mu          sync.Mutex
	sessionID   string
	userID      string
	initialized bool
	emitter     *emitv2.Emitter
	closeOnce   sync.Once
}

// trySend enqueues frame on the write channel without blocking. Drops
// on buffer full so a stuck client cannot block engine producers.
func (c *Conn) trySend(frame []byte) error {
	select {
	case c.send <- frame:
		return nil
	default:
		c.logger.Warn("write buffer full, dropping frame")
		return errBackpressure
	}
}

// close terminates the connection (idempotent).
func (c *Conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.ws.Close(websocket.StatusNormalClosure, "server shutdown")
	})
}

// writePump drains send channel, emits periodic WS pings.
func (c *Conn) writePump(ctx context.Context) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case data := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				c.logger.Warn("ws write failed", zap.Error(err))
				return
			}
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.logger.Debug("ws ping failed", zap.Error(err))
				return
			}
		}
	}
}

// readPump consumes client frames and dispatches them. Pre-init only
// session.create + ping are accepted; everything else returns an error
// frame.
func (c *Conn) readPump(ctx context.Context) {
	defer func() {
		if c.initialized {
			c.ch.registry.unregister(c.sessionID, c.id)
		}
		c.close()
	}()

	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure &&
				!errors.Is(ctx.Err(), context.Canceled) {
				c.logger.Warn("ws read error", zap.Error(err))
			}
			return
		}

		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			c.sendError("invalid_input", "failed to parse frame: "+err.Error())
			continue
		}

		// Pre-init gate.
		if !c.initialized {
			switch head.Type {
			case "session.create":
				c.handleSessionCreate(data)
			case "ping":
				c.respondPong()
			default:
				c.sendError("invalid_input", "send session.create first; got "+head.Type)
			}
			continue
		}

		switch head.Type {
		case "session.create":
			c.sendError("invalid_input", "session already initialised")
		case "ping":
			c.respondPong()
		case "user.message":
			c.handleUserMessage(ctx, data)
		case "tool.result":
			c.handleToolResult(ctx, data)
		case "prompt.user_response":
			c.handlePromptResponse(ctx, data)
		case "session.resume":
			c.handleSessionResume(data)
		case "session.interrupt":
			// no engine API today; log only
			c.logger.Info("session.interrupt received", zap.String("session", c.sessionID))
		default:
			c.sendError("invalid_input", "unknown frame type: "+head.Type)
		}
	}
}

// handleSessionCreate processes the first frame on a new connection,
// allocates an Emitter and registers under the session.
func (c *Conn) handleSessionCreate(raw []byte) {
	var f struct {
		SessionID string `json:"session_id"`
		UserID    string `json:"user_id"`
	}
	_ = json.Unmarshal(raw, &f)
	sessionID := f.SessionID
	if sessionID == "" {
		sessionID = "sess_" + c.id
	}

	em := emitv2.New(emitv2.EmitterConfig{
		Sink:      &connSink{conn: c},
		Sequencer: c.ch.sequencer,
		Lifecycle: c.ch.tracker,
		Artifacts: emitv2.NewArtifactRegistry(),
		SessionID: sessionID,
		AgentID:   "main",
		AgentRole: emitv2.RolePersona,
	})

	c.mu.Lock()
	c.sessionID = sessionID
	c.userID = f.UserID
	c.initialized = true
	c.emitter = em
	c.mu.Unlock()

	c.ch.registry.register(sessionID, c)

	em.SessionEvent("opened", emitv2.SessionOpenedPayload{
		ProtocolVersion: "v2.2",
		Capabilities: map[string]bool{
			"streaming":    true,
			"tools":        true,
			"client_tools": true,
			"sub_agents":   true,
			"plan_review":  true,
			"artifacts":    true,
			"recovery":     c.ch.prompter != nil && c.ch.resumer != nil,
		},
	})

	// Recovery: if this session has unanswered prompts (server crashed
	// after emit but before user answered), re-emit them on the new
	// connection so the client UI can re-render the question/permission/
	// plan_review modal even if it lost its in-memory state.
	if c.ch.prompter != nil {
		if waits, err := c.ch.prompter.ListSession(context.Background(), sessionID); err == nil {
			for _, w := range waits {
				if len(w.PromptFrame) == 0 {
					continue
				}
				_ = c.trySend(append(w.PromptFrame, '\n'))
			}
		}
	}
}

// handleUserMessage parses user.message and dispatches to the engine
// MessageHandler attached to the channel.
func (c *Conn) handleUserMessage(ctx context.Context, raw []byte) {
	var f struct {
		Text             string                 `json:"text"`
		Content          []userContentBlock     `json:"content"`
		CoordinatorMode  string                 `json:"coordinator_mode"`
		PlanConfirmation string                 `json:"plan_confirmation"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		c.sendError("invalid_input", "user.message parse: "+err.Error())
		return
	}
	in := &types.IncomingMessage{
		ChannelName:      "websocket",
		SessionID:        c.sessionID,
		UserID:           c.userID,
		Text:             f.Text,
		CoordinatorMode:  f.CoordinatorMode,
		PlanConfirmation: f.PlanConfirmation,
	}
	if len(f.Content) > 0 {
		blocks := make([]types.IncomingContentBlock, 0, len(f.Content))
		for _, b := range f.Content {
			ib := types.IncomingContentBlock{Type: b.Type, Text: b.Text}
			if b.Source != nil {
				ib.Path = b.Source.Path
				ib.URL = b.Source.URL
				ib.Data = b.Source.Data
				ib.MIMEType = b.Source.MediaType
			}
			blocks = append(blocks, ib)
		}
		in.Content = blocks
		if in.Text == "" {
			for _, b := range f.Content {
				if b.Type == "text" {
					in.Text += b.Text
				}
			}
		}
	}
	if err := c.ch.handler(ctx, in); err != nil {
		c.sendError("internal", err.Error())
	}
}

// handleToolResult parses tool.result and dispatches to engine handler.
func (c *Conn) handleToolResult(ctx context.Context, raw []byte) {
	var f struct {
		ToolUseID string            `json:"tool_use_id"`
		Status    string            `json:"status"`
		Output    string            `json:"output"`
		Error     *emitv2.ErrorInfo `json:"error"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		c.sendError("invalid_input", "tool.result parse: "+err.Error())
		return
	}
	in := &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   c.sessionID,
		UserID:      c.userID,
		ToolResult: &types.ToolResultPayload{
			ToolUseID: f.ToolUseID,
			Status:    f.Status,
			Output:    f.Output,
		},
	}
	if f.Error != nil {
		in.ToolResult.ErrorCode = string(f.Error.Type)
		in.ToolResult.ErrorMessage = f.Error.Message
	}
	if err := c.ch.handler(ctx, in); err != nil {
		c.sendError("internal", err.Error())
	}
}

// handlePromptResponse handles permission / question / plan_review replies.
//
// Routing precedence (one prompt.user_response can only be one of these):
//
//  1. AskUserQuestion (translator-tracked) → bridge to tool.result so the
//     engine's askUserQuestion tool unblocks. The user's selected
//     options / custom text become the tool's Output string.
//  2. plan_review (payload has updated_steps or reason) → PlanResponse.
//  3. fallback: permission → PermissionResponse.
func (c *Conn) handlePromptResponse(ctx context.Context, raw []byte) {
	var f struct {
		RequestID string          `json:"request_id"`
		Decision  string          `json:"decision"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		c.sendError("invalid_input", "prompt.user_response parse: "+err.Error())
		return
	}
	approved := f.Decision == "approved"

	in := &types.IncomingMessage{
		ChannelName: "websocket",
		SessionID:   c.sessionID,
		UserID:      c.userID,
	}

	// Path 1: AskUserQuestion bridge.
	if toolUseID := c.ch.translator.ResolveAskQuestion(c.sessionID, f.RequestID); toolUseID != "" {
		status := "success"
		if !approved {
			status = "cancelled"
		}
		output, errMsg := decodeQuestionAnswer(f.Payload, approved)
		in.ToolResult = &types.ToolResultPayload{
			ToolUseID:    toolUseID,
			Status:       status,
			Output:       output,
			ErrorMessage: errMsg,
		}
		if err := c.ch.handler(ctx, in); err != nil {
			c.sendError("internal", err.Error())
		}
		c.forgetWait(ctx, f.RequestID) // live path: also delete from SQLite to prevent accumulation
		return
	}

	// Path 2: plan_review. Authoritative routing is the translator's
	// pendingPlan map (we know it's a plan_review iff we minted a
	// request_id for one). Heuristics like "payload has updated_steps"
	// are unreliable: a user who approves the plan as-is sends an empty
	// payload and would otherwise be misrouted as a permission reply.
	if planID := c.ch.translator.ResolvePlanReview(c.sessionID, f.RequestID); planID != "" {
		var planShape struct {
			UpdatedSteps []types.ProposedStep `json:"updated_steps"`
			Reason       string               `json:"reason"`
		}
		_ = json.Unmarshal(f.Payload, &planShape)
		in.PlanResponse = &types.PlanResponse{
			PlanID:       planID, // engine-side plan_id, NOT the v2.2 request_id
			Approved:     approved,
			UpdatedSteps: planShape.UpdatedSteps,
			Reason:       planShape.Reason,
		}
		if err := c.ch.handler(ctx, in); err != nil {
			c.sendError("internal", err.Error())
		}
		c.forgetWait(ctx, f.RequestID)
		return
	}

	// Path 3: permission. Same authoritative routing — only honour
	// when the translator confirms it tracked this request_id as a
	// permission prompt. Otherwise the response is for an unknown /
	// expired prompt and we drop with an error frame.
	if engineReqID := c.ch.translator.ResolvePermission(c.sessionID, f.RequestID); engineReqID != "" {
		var perm struct {
			Scope   string `json:"scope"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(f.Payload, &perm)
		in.PermissionResponse = &types.PermissionResponse{
			RequestID: engineReqID, // engine-side perm_xxx, NOT the v2.2 request_id
			Approved:  approved,
			Scope:     types.PermissionScope(perm.Scope),
			Message:   perm.Message,
		}
		if err := c.ch.handler(ctx, in); err != nil {
			c.sendError("internal", err.Error())
		}
		c.forgetWait(ctx, f.RequestID)
		return
	}

	// Path 4 (recovery): in-memory miss but the wait may live in the
	// persisted store. This fires when the server restarted between
	// emit and reply: the live askQuestion / pendingPlan / pendingPerm
	// maps are empty (fresh process) but the on-disk wait record is
	// still there.
	if c.ch.prompter != nil && c.ch.resumer != nil {
		w, lookupErr := c.ch.prompter.Lookup(ctx, f.RequestID)
		if lookupErr == nil && w != nil {
			ans := decodePromptAnswer(w.Kind, approved, f.Payload)
			if err := c.ch.resumer.Resume(ctx, w, ans); err != nil {
				c.sendError("internal", "resume failed: "+err.Error())
				return
			}
			// Wait is now consumed; delete to prevent replay.
			_ = c.ch.prompter.Forget(ctx, f.RequestID)
			return
		}
	}

	c.sendError("invalid_input", "prompt.user_response carries unknown request_id (expired or never sent): "+f.RequestID)
}

// forgetWait deletes a persisted wait after the live path successfully
// dispatched the answer to the engine. Called from all three live
// paths (question / plan_review / permission). Without this, SQLite's
// pending_waits table would accumulate one row per answered prompt
// forever — TTL would still cap it at 24h but routine traffic would
// keep the table large.
func (c *Conn) forgetWait(ctx context.Context, requestID string) {
	if c.ch.prompter == nil {
		return
	}
	_ = c.ch.prompter.Forget(ctx, requestID)
}

// decodePromptAnswer normalises a prompt.user_response payload into the
// kind-agnostic wait.Answer shape consumed by the engine resumer.
func decodePromptAnswer(kind wait.Kind, approved bool, raw []byte) wait.Answer {
	a := wait.Answer{Decision: "denied"}
	if approved {
		a.Decision = "approved"
	}
	if !approved {
		return a
	}
	switch kind {
	case wait.KindQuestion:
		var q struct {
			SelectedOptions []string `json:"selected_options"`
			CustomText      string   `json:"custom_text"`
		}
		_ = json.Unmarshal(raw, &q)
		a.Output, _ = decodeAnswerForKindQuestion(q.SelectedOptions, q.CustomText)
	case wait.KindPlanReview:
		var pl struct {
			UpdatedSteps []any  `json:"updated_steps"`
			Reason       string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &pl)
		a.Output = pl.Reason
		if len(pl.UpdatedSteps) > 0 {
			a.Edits = map[string]any{"updated_steps": pl.UpdatedSteps}
		}
	case wait.KindPermission:
		var perm struct {
			Scope   string `json:"scope"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &perm)
		a.Output = perm.Message
		if perm.Scope != "" {
			a.Edits = map[string]any{"scope": perm.Scope}
		}
	}
	return a
}

func decodeAnswerForKindQuestion(selected []string, custom string) (string, bool) {
	switch {
	case len(selected) > 0 && custom != "":
		return joinComma(selected) + "; " + custom, true
	case len(selected) > 0:
		return joinComma(selected), true
	case custom != "":
		return custom, true
	}
	return "", false
}

// decodeQuestionAnswer turns a prompt.user_response payload (from a
// kind=question prompt) into a string the engine's askUserQuestion tool
// can consume as Output, plus an optional error message when the user
// cancelled.
func decodeQuestionAnswer(payload []byte, approved bool) (output, errMsg string) {
	if !approved {
		return "", "user cancelled"
	}
	if len(payload) == 0 {
		return "", ""
	}
	var ans struct {
		SelectedOptions []string `json:"selected_options"`
		CustomText      string   `json:"custom_text"`
	}
	if err := json.Unmarshal(payload, &ans); err != nil {
		// Caller may have sent the answer as a bare string — fall back.
		var s string
		if err2 := json.Unmarshal(payload, &s); err2 == nil {
			return s, ""
		}
		return string(payload), ""
	}
	switch {
	case len(ans.SelectedOptions) > 0 && ans.CustomText != "":
		return joinComma(ans.SelectedOptions) + "; " + ans.CustomText, ""
	case len(ans.SelectedOptions) > 0:
		return joinComma(ans.SelectedOptions), ""
	case ans.CustomText != "":
		return ans.CustomText, ""
	}
	return "", ""
}

func joinComma(xs []string) string {
	out := ""
	for i, s := range xs {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// handleSessionResume — no replay buffer yet; reply with not_implemented.
func (c *Conn) handleSessionResume(raw []byte) {
	var f struct {
		TraceID string `json:"trace_id"`
	}
	_ = json.Unmarshal(raw, &f)
	if c.emitter == nil {
		return
	}
	c.emitter.SessionEvent("resume_failed", emitv2.SessionResumeFailedPayload{
		TraceID: f.TraceID,
		Reason:  "not_implemented",
	})
}

// respondPong sends a session.event(kind=pong).
func (c *Conn) respondPong() {
	if c.emitter != nil {
		c.emitter.SessionEvent("pong", nil)
		return
	}
	frame := jsonMust(map[string]any{
		"type":    "session.event",
		"payload": map[string]any{"kind": "pong"},
	})
	_ = c.trySend(append(frame, '\n'))
}

// sendError sends a session.event(kind=error) frame.
func (c *Conn) sendError(typ, message string) {
	if c.emitter != nil {
		c.emitter.SessionEvent("error", map[string]any{
			"error": emitv2.NewError(emitv2.ErrorType(typ), message),
		})
		return
	}
	frame := jsonMust(map[string]any{
		"type": "session.event",
		"payload": map[string]any{
			"kind": "error",
			"inner": map[string]any{
				"error": map[string]any{"type": typ, "message": message},
			},
		},
	})
	_ = c.trySend(append(frame, '\n'))
}

// connSink implements emitv2.Sink by writing JSON frames into a Conn's
// send queue. Backpressure: drops card.tick under buffer pressure;
// other event types fall through to the trySend warning + drop.
type connSink struct{ conn *Conn }

func (s *connSink) Send(e emitv2.Event) {
	frame, err := json.Marshal(e)
	if err != nil {
		return // unreachable for well-formed events
	}
	frame = append(frame, '\n')
	_ = s.conn.trySend(frame)
}

// userContentBlock is the wire shape of a user.message content block.
type userContentBlock struct {
	Type   string         `json:"type"`
	Text   string         `json:"text"`
	Source *contentSource `json:"source"`
}

type contentSource struct {
	Type      string `json:"type"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	Data      string `json:"data"`
	MediaType string `json:"media_type"`
}

var errBackpressure = errors.New("ws: backpressure")
