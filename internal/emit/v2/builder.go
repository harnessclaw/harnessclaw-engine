package emitv2

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Emitter is a per-agent producer. One Emitter per (session, agent_id,
// agent_run) instance. Created by Engine.NewEmitter or via Sub for a
// child agent scope.
//
// The Emitter owns a parent stack: when a card is opened (Add) it gets
// pushed; on Close it gets popped. Subsequent Add calls auto-set
// ParentCardID to the stack top — the framework promise that no caller
// has to thread parent pointers through every emit site.
//
// Thread-safety: Add / Set / Append / Tick / Close are safe to call from
// multiple goroutines. The parent stack is mutex-protected.
type Emitter struct {
	mu sync.Mutex

	sink      Sink
	seq       *Sequencer
	lifecycle *Tracker
	artifacts *ArtifactRegistry
	now       func() time.Time // injectable for tests

	sessionID  string
	traceID    string
	agentID    string
	agentRole  AgentRole
	agentRunID string

	// parents is the lifecycle stack for ParentCardID auto-injection.
	// Push on Add; pop on Close. Concurrent cards (parallel tools) may
	// briefly reorder, so callers can override via WithParent.
	parents []string
}

// EmitterConfig is the constructor input.
type EmitterConfig struct {
	Sink       Sink
	Sequencer  *Sequencer
	Lifecycle  *Tracker          // nil = no orphan watchdog
	Artifacts  *ArtifactRegistry // nil = no auto-rewrite of artifact references in message text
	SessionID  string
	TraceID    string
	AgentID    string
	AgentRole  AgentRole
	AgentRunID string
	Now        func() time.Time // optional clock override (tests)
}

// New constructs a new Emitter. Sink and SessionID are required.
// Sequencer defaults to a fresh one if nil. TraceID is auto-allocated if
// empty. AgentRole defaults to RolePersona. Now defaults to time.Now.
func New(cfg EmitterConfig) *Emitter {
	if cfg.Sink == nil {
		panic("emitv2.New: Sink is required")
	}
	if cfg.SessionID == "" {
		panic("emitv2.New: SessionID is required")
	}
	if cfg.Sequencer == nil {
		cfg.Sequencer = NewSequencer()
	}
	if cfg.TraceID == "" {
		cfg.TraceID = NewTraceID()
	}
	if cfg.AgentRole == "" {
		cfg.AgentRole = RolePersona
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Emitter{
		sink:       cfg.Sink,
		seq:        cfg.Sequencer,
		lifecycle:  cfg.Lifecycle,
		artifacts:  cfg.Artifacts,
		now:        cfg.Now,
		sessionID:  cfg.SessionID,
		traceID:    cfg.TraceID,
		agentID:    cfg.AgentID,
		agentRole:  cfg.AgentRole,
		agentRunID: cfg.AgentRunID,
	}
}

// Sub returns a child Emitter for a sub-agent scope. The child shares
// session, trace, sequencer, lifecycle tracker, sink, and clock with the
// parent (so events from sub-agents stay on the same trace and ordered
// with parent events) but binds its own agent identity. The child's
// initial parent stack is set to the parent emitter's current top —
// meaning the sub-agent's first card.add will auto-attach as a child of
// the most recently opened card on the parent.
//
// This is the **single mechanism** that enforces the v2.2 contract
// "envelope.agent_id is correct in nested sub-agent scopes": new agents
// only ever come from this method, and AgentID is bound at construction.
func (e *Emitter) Sub(agentID string, role AgentRole, runID string) *Emitter {
	e.mu.Lock()
	var top string
	if n := len(e.parents); n > 0 {
		top = e.parents[n-1]
	}
	e.mu.Unlock()

	child := &Emitter{
		sink:       e.sink,
		seq:        e.seq,
		lifecycle:  e.lifecycle,
		artifacts:  e.artifacts, // share registry across agent scopes within a trace
		now:        e.now,
		sessionID:  e.sessionID,
		traceID:    e.traceID,
		agentID:    agentID,
		agentRole:  role,
		agentRunID: runID,
	}
	if top != "" {
		child.parents = []string{top}
	}
	return child
}

// SessionID returns the session this emitter is bound to.
func (e *Emitter) SessionID() string { return e.sessionID }

// TraceID returns the trace this emitter is bound to.
func (e *Emitter) TraceID() string { return e.traceID }

// AgentID returns the agent identity bound to this emitter.
func (e *Emitter) AgentID() string { return e.agentID }

// CardBuilder scopes emit calls to a specific card.
type CardBuilder struct {
	em     *Emitter
	kind   CardKind
	cardID string
}

// Card opens a builder for kind/cardID. cardID is the caller-allocated
// stable identifier; the same Card(kind, id) call produces a Builder that
// targets the same wire card across multiple actions.
func (e *Emitter) Card(kind CardKind, cardID string) *CardBuilder {
	return &CardBuilder{em: e, kind: kind, cardID: cardID}
}

// EmitOpt is a functional option for Add/Set/Append/Tick/Close calls.
type EmitOpt func(*emitState)

type emitState struct {
	parentOverride  string
	severity        Severity
	severitySet     bool
	hint            *Hint
	hintTitleSet    bool
	metrics         *Metrics
	error           *ErrorInfo
	innerPayload    any
	disableLifecyc  bool
	cardKindOverride CardKind
}

// WithParent overrides the auto-detected parent_card_id.
func WithParent(cardID string) EmitOpt {
	return func(s *emitState) { s.parentOverride = cardID }
}

// WithSeverity overrides the registry-derived severity.
func WithSeverity(sev Severity) EmitOpt {
	return func(s *emitState) { s.severity = sev; s.severitySet = true }
}

// WithHint overrides Hint fields. Title falls back to registry template
// when unset; Icon falls back to card_kind default.
func WithHint(h Hint) EmitOpt {
	return func(s *emitState) {
		s.hint = &h
		s.hintTitleSet = h.Title != ""
	}
}

// WithMetrics attaches a Metrics block. Only emitted on card.close.
func WithMetrics(m Metrics) EmitOpt {
	return func(s *emitState) { s.metrics = &m }
}

// WithError attaches an ErrorInfo block. Only meaningful on
// card.close{status:failed} or session.event{kind:error}. The Builder
// auto-fills UserMessage/Retryable from the registry if the caller
// supplied an ErrorInfo without them.
func WithError(err *ErrorInfo) EmitOpt {
	return func(s *emitState) {
		if err == nil {
			return
		}
		// Registry tail-fill.
		if err.UserMessage == "" || !err.Retryable {
			meta := LookupErrorMeta(err.Type)
			if err.UserMessage == "" {
				err.UserMessage = meta.DefaultUserMessage
			}
			if !err.Retryable && meta.DefaultRetryable {
				err.Retryable = true
			}
		}
		s.error = err
	}
}

// WithInner attaches a custom inner payload. Used for prompt.user /
// session.event / card.tick where the payload has a kind-discriminated
// inner field.
func WithInner(p any) EmitOpt {
	return func(s *emitState) { s.innerPayload = p }
}

// WithoutLifecycle disables orphan watchdog tracking for this card. Used
// for cards that follow a non-standard lifecycle (e.g. card.add immediately
// followed by card.close in the same call).
func WithoutLifecycle() EmitOpt {
	return func(s *emitState) { s.disableLifecyc = true }
}

// applyOpts runs all options against a fresh state.
func applyOpts(opts []EmitOpt) emitState {
	var s emitState
	for _, o := range opts {
		o(&s)
	}
	return s
}

// ----------------- Card actions -----------------

// Add emits a card.add for this card. payload is the typed payload struct
// (e.g. ToolPayload, AgentPayload). The framework records the card in
// the lifecycle tracker and pushes onto the parent stack so subsequent
// child cards auto-attach. Any ArtifactRef found in payload is recorded
// into the artifact registry so future ChannelText appends auto-rewrite
// to artifact:// URIs (§11).
func (b *CardBuilder) Add(payload any, opts ...EmitOpt) {
	s := applyOpts(opts)
	parent := b.em.popParentOverrideOrTop(s.parentOverride)

	hint := b.buildHint(payload, &s)
	env := b.buildEnvelope(parent, &s, false)

	b.em.recordArtifactsFromPayload(payload, b.kind)

	ev := Event{
		Type:     EventCardAdd,
		Envelope: env,
		Hint:     hint,
		Payload:  payload,
	}

	b.em.sink.Send(ev)

	// Push onto parent stack; track lifecycle.
	b.em.pushParent(b.cardID)
	if b.em.lifecycle != nil {
		registryTimeout := OrphanTimeout(b.kind)
		switch {
		case s.disableLifecyc && registryTimeout > 0:
			// Explicitly opted out of the orphan watchdog while still
			// being a lifecycle-tracked kind (e.g. scheduler / task
			// tool cards that wrap a multi-minute sub-agent run). We
			// register a "chain-only" entry — timeout=0 tells the
			// sweep loop to skip it, but Touch can still walk through
			// to refresh ancestors above. Without this, any heartbeat
			// emitted by descendants of this card would dead-end here,
			// and the turn card above could orphan-timeout mid-run.
			b.em.lifecycle.Open(b.cardID, b.kind, 0, parent, b)
		case !s.disableLifecyc && registryTimeout > 0:
			// Normal tracked card. Pass the explicit parent we just put
			// on the wire so the tracker's heartbeat chain matches what
			// the renderer sees, even when WithParent was used to
			// override the stack top.
			b.em.lifecycle.Open(b.cardID, b.kind, registryTimeout, parent, b)
		}
		// Opening a child counts as activity on the parent chain —
		// reset their deadlines so a long-running parent isn't killed
		// just because it spent most of its time waiting on children.
		b.em.lifecycle.Touch(parent)
	}
}

// Set emits a card.set for this card. patch is a partial payload — the
// renderer overwrites only the fields present.
func (b *CardBuilder) Set(patch any, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := b.buildEnvelope(s.parentOverride, &s, false)

	ev := Event{
		Type:     EventCardSet,
		Envelope: env,
		Payload:  patch,
	}
	b.em.sink.Send(ev)
	if b.em.lifecycle != nil {
		b.em.lifecycle.Touch(b.cardID)
	}
}

// Append emits a card.append for streaming content. channel selects which
// content track of the card to append into.
//
// Artifact:// URI rewrite (v2.2 §11): when channel=text and the message
// is on a persona card, the chunk is scanned for known artifact names
// (recorded by earlier card.add events) and rewritten to markdown URIs.
// This is the "framework-side hard constraint" that makes prompt drift
// unable to leak raw artifact IDs or bare filenames into user-facing
// text.
func (b *CardBuilder) Append(channel Channel, chunk string, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := b.buildEnvelope(s.parentOverride, &s, false)

	if channel == ChannelText && b.em.artifacts != nil && b.kind == CardMessage {
		chunk = b.em.artifacts.Rewrite(chunk)
	}

	pl := AppendPayload{Channel: channel}
	if channel == ChannelToolInput {
		pl.PartialJSON = chunk
	} else {
		pl.Chunk = chunk
	}

	ev := Event{
		Type:     EventCardAppend,
		Envelope: env,
		Payload:  pl,
	}
	b.em.sink.Send(ev)
	if b.em.lifecycle != nil {
		b.em.lifecycle.Touch(b.cardID)
	}
}

// AppendIndexed is Append with an explicit content-block index, for
// multi-block messages where the same channel can run in parallel.
func (b *CardBuilder) AppendIndexed(channel Channel, index int, chunk string, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := b.buildEnvelope(s.parentOverride, &s, false)

	pl := AppendPayload{Channel: channel, Index: index}
	if channel == ChannelToolInput {
		pl.PartialJSON = chunk
	} else {
		pl.Chunk = chunk
	}

	ev := Event{
		Type:     EventCardAppend,
		Envelope: env,
		Payload:  pl,
	}
	b.em.sink.Send(ev)
	if b.em.lifecycle != nil {
		b.em.lifecycle.Touch(b.cardID)
	}
}

// Tick emits a card.tick (throttled / dropable). inner is the kind-specific
// payload (ProgressPayload, HeartbeatPayload, IntentPayload, NotePayload,
// EscalationPayload).
func (b *CardBuilder) Tick(kind TickKind, inner any, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := b.buildEnvelope(s.parentOverride, &s, false)

	ev := Event{
		Type:     EventCardTick,
		Envelope: env,
		Payload:  TickPayload{Kind: kind, Inner: inner},
	}
	b.em.sink.Send(ev)
	if b.em.lifecycle != nil {
		b.em.lifecycle.Touch(b.cardID)
	}
}

// recordArtifactsFromPayload extracts ArtifactRef slices from typed
// payloads and registers them. This is what makes future text appends
// auto-rewrite work — any tool/agent/step that produces artifacts
// surfaces them here.
func (e *Emitter) recordArtifactsFromPayload(payload any, kind CardKind) {
	if e.artifacts == nil {
		return
	}
	switch p := payload.(type) {
	case ToolPayload:
		e.artifacts.RecordRefs(p.Artifacts)
	case *ToolPayload:
		if p != nil {
			e.artifacts.RecordRefs(p.Artifacts)
		}
	case AgentPayload:
		e.artifacts.RecordRefs(p.Artifacts)
	case *AgentPayload:
		if p != nil {
			e.artifacts.RecordRefs(p.Artifacts)
		}
	case StepPayload:
		e.artifacts.RecordRefs(p.Artifacts)
	case *StepPayload:
		if p != nil {
			e.artifacts.RecordRefs(p.Artifacts)
		}
	case ArtifactPayload:
		e.artifacts.Record(p.Name, p.ArtifactID)
	case *ArtifactPayload:
		if p != nil {
			e.artifacts.Record(p.Name, p.ArtifactID)
		}
	case ClosePayload:
		e.recordArtifactsFromPayload(p.Inner, kind)
	}
}

// Close emits a card.close with status. The card is removed from the
// lifecycle tracker and popped off the parent stack.
//
// Idempotency: calling Close twice on the same card_id emits two events.
// The lifecycle tracker only tracks the first; subsequent closes do not
// trigger orphan-timeout cleanup. Renderer behaviour: most-recent close
// wins (same card_id).
func (b *CardBuilder) Close(status Status, opts ...EmitOpt) {
	s := applyOpts(opts)
	if !s.severitySet {
		s.severity = SeverityForClose(status)
		s.severitySet = true
	}
	env := b.buildEnvelope(s.parentOverride, &s, true)

	pl := ClosePayload{Status: status}
	if s.error != nil {
		pl.Error = s.error
	}
	if s.innerPayload != nil {
		pl.Inner = s.innerPayload
	}

	// Close payloads can also carry artifacts (tool/agent close inner
	// payloads commonly do). Record them so subsequent text appends
	// in the same trace can rewrite references.
	b.em.recordArtifactsFromPayload(s.innerPayload, b.kind)

	ev := Event{
		Type:     EventCardClose,
		Envelope: env,
		Metrics:  s.metrics,
		Payload:  pl,
	}
	b.em.sink.Send(ev)

	// Close removes this card from the tracker and pushes the heartbeat
	// up: the parent just witnessed a child finish, which is also an
	// activity signal that should reset its deadline.
	parent := ""
	if b.em.lifecycle != nil {
		parent = b.em.lifecycle.ParentOf(b.cardID)
		b.em.lifecycle.Close(b.cardID)
		if parent != "" {
			b.em.lifecycle.Touch(parent)
		}
	}
	b.em.popParent(b.cardID)
}

// ----------------- Non-card emits -----------------

// PromptUser emits a prompt.user event asking the user to respond. The
// caller passes the kind ("permission" / "question" / "plan_review") and
// the kind-specific inner payload. RequestID is auto-allocated.
func (e *Emitter) PromptUser(kind string, inner any, opts ...EmitOpt) string {
	requestID := NewRequestID()
	e.PromptUserWithID(requestID, kind, inner, opts...)
	return requestID
}

// PromptUserWithID is the same as PromptUser but uses a caller-supplied
// request_id. Used by the recovery path: the channel translator
// pre-allocates the id, persists the wait under that id, then emits
// — so a server crash between persist and emit cannot produce a wait
// the wire never carried.
func (e *Emitter) PromptUserWithID(requestID, kind string, inner any, opts ...EmitOpt) {
	s := applyOpts(opts)

	env := e.envelope("", "", "", &s, false) // prompt has no card_id
	env.Severity = SeverityInfo

	e.sink.Send(Event{
		Type:     EventPromptUser,
		Envelope: env,
		Payload: PromptUserPayload{
			RequestID: requestID,
			Kind:      kind,
			Inner:     inner,
		},
	})
}

// PromptReply emits a prompt.reply echo after the user has responded.
func (e *Emitter) PromptReply(requestID, decision, reason string, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := e.envelope("", "", "", &s, false)
	env.Severity = SeverityInfo

	e.sink.Send(Event{
		Type:     EventPromptReply,
		Envelope: env,
		Payload:  PromptReplyPayload{RequestID: requestID, Decision: decision, Reason: reason},
	})
}

// SessionEvent emits a session.event with the given kind ("opened",
// "updated", "error", "resumed", "resume_failed") and inner payload.
func (e *Emitter) SessionEvent(kind string, inner any, opts ...EmitOpt) {
	s := applyOpts(opts)
	env := e.envelope("", "", "", &s, false)
	if kind == "error" {
		env.Severity = SeverityError
	} else {
		env.Severity = SeverityInfo
	}

	e.sink.Send(Event{
		Type:     EventSession,
		Envelope: env,
		Payload:  SessionPayload{Kind: kind, Inner: inner},
	})
}

// ----------------- internals -----------------

// buildEnvelope assembles the Envelope for a card-scoped event (Add, Set,
// Append, Tick, Close). isClose true means we apply close-specific
// severity defaults already handled by Close().
func (b *CardBuilder) buildEnvelope(parentOverride string, s *emitState, _ bool) Envelope {
	parent := parentOverride
	if parent == "" {
		// For card.set/append/tick/close, parent is read from the lifecycle
		// tracker (the open card's parent). Add() handles parent specially.
		parent = b.em.lookupParent(b.cardID)
	}
	return b.em.envelope(b.cardID, parent, b.kind, s, true)
}

func (e *Emitter) envelope(cardID, parentCardID string, kind CardKind, s *emitState, hasCard bool) Envelope {
	role := e.agentRole
	severity := SeverityInfo
	if hasCard {
		role = effectiveRole(role, kind)
	}
	if s.severitySet {
		severity = s.severity
	}
	env := Envelope{
		EventID:      NewEventID(),
		SessionID:    e.sessionID,
		TraceID:      e.traceID,
		CardID:       cardID,
		ParentCardID: parentCardID,
		CardKind:     kind,
		Seq:          e.seq.Next(e.traceID),
		Timestamp:    e.now().UTC(),
		AgentID:      e.agentID,
		AgentRole:    role,
		AgentRunID:   e.agentRunID,
		Severity:     severity,
	}
	return env
}

// effectiveRole picks the AgentRole to write on envelope: the emitter's
// bound role takes precedence (it knows the agent), but when the role is
// unset the registry's per-card-kind default fills in.
func effectiveRole(bound AgentRole, kind CardKind) AgentRole {
	if bound != "" {
		return bound
	}
	return LookupCardMeta(kind).DefaultRole
}

// buildHint assembles the Hint for an Add event. Title is registry-templated
// when caller didn't provide one; Icon falls back to the registry default.
func (b *CardBuilder) buildHint(payload any, s *emitState) *Hint {
	meta := LookupCardMeta(b.kind)
	h := Hint{}
	if s.hint != nil {
		h = *s.hint
	}
	if h.Title == "" {
		h.Title = renderTitle(meta.TitleTpl, payload)
	}
	if h.Icon == "" {
		h.Icon = meta.DefaultIcon
	}
	return &h
}

// renderTitle expands {token} placeholders in the template. Tokens are
// looked up by JSON-tag name on payload (after json.Marshal) so the same
// substitution rule works for any payload struct without reflection.
func renderTitle(tpl string, payload any) string {
	if tpl == "" {
		return ""
	}
	if !strings.Contains(tpl, "{") {
		return tpl
	}
	// Marshal payload to JSON then unmarshal as map for cheap field lookup.
	// Used only on Add (not on hot path of streaming chunks).
	if payload == nil {
		return tpl
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return tpl
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return tpl
	}
	out := tpl
	for k, v := range m {
		key := "{" + k + "}"
		if !strings.Contains(out, key) {
			continue
		}
		out = strings.ReplaceAll(out, key, fmt.Sprintf("%v", v))
	}
	return out
}

// ----------------- parent stack -----------------

// pushParent records cardID as the most recent open card.
func (e *Emitter) pushParent(cardID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.parents = append(e.parents, cardID)
}

// popParent removes cardID from the parent stack. If cardID is not at the
// top (concurrent cards closing out of order), it's removed from wherever
// it sits.
func (e *Emitter) popParent(cardID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.parents) - 1; i >= 0; i-- {
		if e.parents[i] == cardID {
			e.parents = append(e.parents[:i], e.parents[i+1:]...)
			return
		}
	}
}

// popParentOverrideOrTop returns override if non-empty, else the current
// parent stack top.
func (e *Emitter) popParentOverrideOrTop(override string) string {
	if override != "" {
		return override
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := len(e.parents); n > 0 {
		return e.parents[n-1]
	}
	return ""
}

// lookupParent returns the parent recorded by the lifecycle tracker for
// cardID. Used by card.set/append/tick/close so each emit on an open
// card carries the correct parent_card_id even when other cards have
// since been opened.
func (e *Emitter) lookupParent(cardID string) string {
	if e.lifecycle == nil {
		return ""
	}
	return e.lifecycle.ParentOf(cardID)
}

// SuspendChainFromCard pauses the orphan watchdog for cardID and every
// still-open ancestor up the parent chain. Returns the list of card IDs
// that were actually paused (so the caller can hand it to ResumeChain
// once the wait ends). No-op if the emitter has no lifecycle tracker.
//
// Used by the channel translator on prompt.user emission: the user is
// reviewing, so the surrounding agent / message / turn cards are
// intentionally idle and must not orphan-timeout.
func (e *Emitter) SuspendChainFromCard(cardID string) []string {
	if e.lifecycle == nil {
		return nil
	}
	return e.lifecycle.SuspendChain(cardID)
}

// ResumeChain reverses SuspendChainFromCard. Pass the slice that
// SuspendChainFromCard returned. No-op when the emitter has no lifecycle
// tracker or the slice is empty.
func (e *Emitter) ResumeChain(cardIDs []string) {
	if e.lifecycle == nil {
		return
	}
	e.lifecycle.ResumeChain(cardIDs)
}
